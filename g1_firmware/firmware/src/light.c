/* The ambient light sensor (OPT3007 on TWIM1 at 0x45, register-compatible with the OPT3001; review-v5.md §8.4).
 * It is powered with the panel's rails (stock's power_on_opt3007 powers the panel), so it is found during the
 * boot's panel check and set up again at each use; as stock, it converts only while the panel is on, and only
 * while something uses it (AUTO_BRIGHTNESS, a running program, the LIGHT EVENTS bit; VM.md §6.6): continuous
 * 800 ms conversions with the automatic full-scale range, else shut down, and LIGHT reads UNKNOWN. The OPT3007 has no interrupt line, so a thread
 * reads each conversion; the LIGHT trigger is the VM's range check on these readings. With AUTO_BRIGHTNESS = 1
 * a reading sets BRIGHTNESS through stock's table (VM.md §6.7), a new level taken when two readings in a row
 * ask for one 2 or more away from the current level. */
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(light, LOG_LEVEL_INF);

#define ADDR 0x45
#define REG_RESULT 0x00
#define REG_CONFIG 0x01
#define REG_MANUFACTURER 0x7E
#define REG_DEVICE 0x7F
#define CONFIG_OFF 0xC810   /* the reset value: automatic range, 800 ms, shut down, latched */
#define CONFIG_ON 0xCE10    /* the same, continuous conversions (stock's opt3007_chip_init) */
#define CRF 0x0080          /* conversion ready (cleared by reading the configuration) */
#define CONVERSION_MS 800

static const struct device *bus = DEVICE_DT_GET(DT_NODELABEL(i2c1));
static K_SEM_DEFINE(sem, 0, 1);
static atomic_t want, auto_on;
static bool present;
static volatile bool converting;
static volatile uint32_t lux = VM_UNKNOWN;
static uint32_t readings, i2c_errors;

static int rd(uint8_t reg, uint16_t *v)
{
	uint8_t b[2];
	int err = i2c_write_read(bus, ADDR, &reg, 1, b, 2);

	*v = b[0] << 8 | b[1];
	i2c_errors += err != 0;
	return err;
}

static int wr(uint8_t reg, uint16_t v)
{
	uint8_t b[3] = {reg, v >> 8, v & 0xFF};
	int err = i2c_write(bus, b, 3, ADDR);

	i2c_errors += err != 0;
	return err;
}

/* stock's table (VM.md §6.7), lux to a level 0..42 */
static uint8_t level_for(uint32_t centilux)
{
	uint32_t l = centilux / 100;

	return l < 50 ? l / 5 : l < 200 ? 10 + (l - 50) / 10 : l < 1000 ? 25 + (l - 200) / 50 : l < 2000 ? 41 : 42;
}

/* the second reading in a row 2 or more levels away from the current one sets its level */
static void auto_brightness(uint32_t centilux, bool *away_before)
{
	uint8_t level = level_for(centilux), cur = panel_level(), out[PANEL_LEVEL_LEN];
	bool away = level >= cur + 2 || level + 2 <= cur;

	if (away && *away_before) {
		panel_set_level(level, out);
		peer_kick();
		away = false;
	}
	*away_before = away;
}

static void light_thread(void *a, void *b, void *c);
K_THREAD_DEFINE(light_tid, 1024, light_thread, NULL, NULL, NULL, 7, 0, K_TICKS_FOREVER);

/* the executor, while the boot's panel check has the panel's rails on */
void light_probe(void)
{
	uint16_t m = 0, d = 0;

	if (!device_is_ready(bus) || rd(REG_MANUFACTURER, &m) || rd(REG_DEVICE, &d) || m != 0x5449 || d != 0x3001) {
		LOG_WRN("no light sensor (manufacturer 0x%04x, device 0x%04x)", m, d);
		return;
	}
	present = wr(REG_CONFIG, CONFIG_OFF) == 0;
	LOG_INF("OPT3007 %s", present ? "found" : "not answering");
	if (present) {
		k_thread_start(light_tid);
	}
}

static void light_thread(void *a, void *b, void *c)
{
	uint16_t v;
	bool on = false, away = false;
	int64_t next = 0;

	for (;;) {
		if (!atomic_get(&want)) {
			if (on) {
				if (panel_is_on() || panel_in_standby()) {
					wr(REG_CONFIG, CONFIG_OFF);   /* else its rails are off already */
				}
				on = converting = false;
				lux = VM_UNKNOWN;
				away = false;
				exec_wake();
			}
			k_sem_take(&sem, K_FOREVER);
			continue;
		}
		if (!on) {
			on = converting = wr(REG_CONFIG, CONFIG_ON) == 0;
			next = k_uptime_get() + CONVERSION_MS;
		}
		k_sem_take(&sem, K_TIMEOUT_ABS_MS(next));
		if (!atomic_get(&want) || k_uptime_get() < next) {
			continue;
		}
		if (rd(REG_CONFIG, &v) || !(v & 0x0600)) {
			on = false;   /* the panel's rails went off and on: set it up again */
			continue;
		}
		if (!(v & CRF) || rd(REG_RESULT, &v)) {
			next = k_uptime_get() + 50;
			continue;
		}
		next += CONVERSION_MS;
		lux = (uint32_t)(v & 0xFFF) << (v >> 12);
		readings++;
		if (atomic_get(&auto_on)) {
			auto_brightness(lux, &away);
		}
		exec_wake();
	}
}


/* the executor, after each run: the panel is on and AUTO_BRIGHTNESS, a program or the LIGHT EVENTS bit uses it */
void light_use(bool on)
{
	if (atomic_set(&want, on) != on) {
		k_sem_give(&sem);
	}
}

bool light_present(void)
{
	return present;
}

/* LIGHT, 0.01 lux (the result register's mantissa << exponent) */
uint32_t light_prop(void)
{
	return lux;
}

void light_set_auto(bool on)
{
	atomic_set(&auto_on, on);
}

bool light_auto(void)
{
	return atomic_get(&auto_on);
}

void light_stats(uint32_t *n, uint32_t *errors, bool *on)
{
	*n = readings;
	*errors = i2c_errors;
	*on = converting;
}
