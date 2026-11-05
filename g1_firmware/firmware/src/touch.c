/* The TouchBar controller (AW9320x on TWIM2 at 0x12, review-v5.md §8.1): 16-bit register addresses and 32-bit
 * values, both big endian. Brought up as stock 1.6.6 does: its rail (P0.30) is on from boot, then the chip ID,
 * the wake writes, the ready handshake (event bit 0), its firmware version, stock's 44-entry configuration
 * table (taken from Even's image by scripts/fetch_stock.sh, src/touchcfg.S), the commit and the event enable.
 * Its interrupt line (P1.09, active low) wakes the thread, which reads the event register (a read clears it):
 * bit 0 the controller lost its configuration (loaded again), bits 1 and 2 touch down and up, which become
 * gestures with stock's timings (touch_key_thread), bits 8 and 7 its in-ear / out-of-ear classification
 * (it confirms WORN, below). The controller
 * scans only while something uses it (a running program with a TOUCH or WEAR handler or waiting for one, a
 * TOUCH or WEAR EVENTS bit), else it sleeps; nothing polls, so the CPU sleeps between touches. */
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/gpio.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(touch, LOG_LEVEL_INF);

#define ADDR 0x12
#define REG_FWVER 0x1BA8
#define REG_CMD 0x4408     /* 1 active (also the commit after the table), 2 sleep, 3 deep sleep */
#define REG_IRQSRC 0x4410
#define REG_IRQEN 0x4414
#define REG_MCFG 0x4444
#define REG_WAKE 0x4700
#define REG_CHIPID 0xFF00
#define REG_RSTNALL 0xFF18
#define REG_ACCESS 0xFF20
#define FWVER 0x06000007   /* the controller firmware of shipped arms, which stock's table is for */
#define CMD_ACTIVE 1
#define CMD_SLEEP 2

#define EV_RESET BIT(0)
#define EV_DOWN BIT(1)
#define EV_UP BIT(2)
#define EV_REMOVED BIT(7)
#define EV_WORN BIT(8)

#define TAP_MS 305      /* stock: presses closer than this count as one gesture, reported this long after */
#define HOLD_MS 458     /* stock: held this long, a press is a long press */
#define STUCK_MS 32000  /* stock's long press timeout: the release never came */

#define USER DT_PATH(zephyr_user)

extern const uint8_t g1_touch_table[], g1_touch_table_end[];

/* WORN (VM.md §2.5, §6.7): the network core's link value as stock's check_work_mode reads it (51 and above: worn at
 * once; below 40 for 3 s: not worn; 0 is no reading yet), the controller's worn event setting 1 at once and its
 * out-of-ear event counting only until the net core reported a value. It reads 0 in the case, and after the case
 * until the next classification; a classification as worn since the case lets IN_CASE go (pmic.c). */
static atomic_t worn = ATOMIC_INIT(-1);
static atomic_t after_case;
static int64_t low_since;
static bool link_seen;

static void classified(int v)
{
	atomic_set(&worn, v);
	atomic_set(&after_case, 0);
}

void touch_link_value(uint8_t v)
{
	if (v == 0) {
		return;
	}
	link_seen = true;
	if (v >= 51) {
		classified(1);
		low_since = 0;
	} else if (v >= 40) {
		low_since = 0;
	} else if (!low_since) {
		low_since = k_uptime_get();
	}
}

/* every second (exec_sensors) */
void touch_wear_tick(void)
{
	if (low_since && k_uptime_get() - low_since >= 3000) {
		classified(0);
		low_since = 0;
	}
}

void touch_case_entered(void)
{
	atomic_set(&after_case, 1);
}

bool touch_worn(void)
{
	return atomic_get(&worn) == 1 && !atomic_get(&after_case);
}

/* DIAG CASE: the last classification (-1 none) and whether the arm was in the case since */
int touch_wear_raw(bool *since_case)
{
	*since_case = atomic_get(&after_case);
	return (int)atomic_get(&worn);
}

static uint32_t wear_prop(void)
{
	if (g1_in_case() || atomic_get(&after_case)) {
		return 0;
	}
	return atomic_get(&worn) < 0 ? VM_UNKNOWN : (uint32_t)atomic_get(&worn);
}

#if DT_NODE_HAS_PROP(USER, touch_int_gpios)
static void wear_controller(bool on)
{
	if (on || !link_seen) {
		classified(on ? 1 : 0);
	}
}

static const struct device *bus = DEVICE_DT_GET(DT_NODELABEL(i2c2));
static const struct gpio_dt_spec irq = GPIO_DT_SPEC_GET(USER, touch_int_gpios);
static struct gpio_callback irq_cb;
static K_SEM_DEFINE(sem, 0, 1);
static atomic_t want_active;
static bool present, active;
static uint32_t irqen;
static volatile uint8_t gesture;
static uint32_t events, i2c_errors, loads, irqs;

int touch_reg_read(uint16_t reg, uint32_t *v)
{
	uint8_t a[2] = {reg >> 8, reg & 0xFF}, d[4];
	int err = i2c_write_read(bus, ADDR, a, 2, d, 4);

	*v = err ? 0xFFFFFFFF : (uint32_t)d[0] << 24 | d[1] << 16 | d[2] << 8 | d[3];
	if (err) {
		i2c_errors++;
	}
	return err;
}

int touch_reg_write(uint16_t reg, uint32_t v)
{
	uint8_t d[6] = {reg >> 8, reg & 0xFF, v >> 24, v >> 16, v >> 8, v};
	int err = i2c_write(bus, d, 6, ADDR);

	if (err) {
		i2c_errors++;
	}
	return err;
}

/* stock's table, then the commit (the controller starts scanning) and the event enable, with the wear bits */
static int load(void)
{
	int err = 0;

	for (const uint8_t *p = g1_touch_table; p + 8 <= g1_touch_table_end; p += 8) {
		uint16_t reg = get32(p);

		if (reg == REG_IRQEN) {
			irqen = get32(p + 4) | EV_WORN | EV_REMOVED;
		} else {
			err |= touch_reg_write(reg, get32(p + 4));
		}
	}
	err |= touch_reg_write(REG_CMD, CMD_ACTIVE);
	err |= touch_reg_write(REG_IRQEN, irqen);
	loads++;
	return err;
}

static int bring_up(void)
{
	uint32_t id, v = 0;
	int i;

	if (touch_reg_read(REG_CHIPID, &id) || (id != 0x20223203 && id != 0x20223205 && id != 0x20223207)) {
		LOG_WRN("no TouchBar controller (chip ID 0x%08x)", id);
		return -ENODEV;
	}
	touch_reg_write(REG_ACCESS, 0x3C00F091);
	touch_reg_write(REG_WAKE, 0x110);
	k_msleep(1);
	touch_reg_write(REG_MCFG, 0);
	k_msleep(20);
	touch_reg_write(REG_RSTNALL, 0x3C);
	k_msleep(25);
	touch_reg_write(REG_WAKE, 0x102);
	for (i = 0; i <= 100 && !(touch_reg_read(REG_IRQSRC, &v) == 0 && (v & EV_RESET)); i++) {
		k_msleep(1);
	}
	if (i > 100) {
		LOG_ERR("TouchBar controller 0x%08x: not ready after its wake", id);
		return -ETIMEDOUT;
	}
	touch_reg_read(REG_FWVER, &v);
	if (v != FWVER) {
		LOG_ERR("TouchBar controller firmware 0x%08x, not 0x%08x: left unconfigured", v, FWVER);
		return -ENOTSUP;
	}
	if (load()) {
		return -EIO;
	}
	LOG_INF("TouchBar controller 0x%08x configured (%u entries), events 0x%03x", id,
		(unsigned)(g1_touch_table_end - g1_touch_table) / 8, irqen);
	return 0;
}

static void irq_isr(const struct device *dev, struct gpio_callback *cb, uint32_t pins)
{
	irqs++;
	k_sem_give(&sem);
}

/* the interrupt only while the controller scans */
static void irq_arm(bool on)
{
	gpio_pin_interrupt_configure_dt(&irq, on ? GPIO_INT_EDGE_TO_ACTIVE : GPIO_INT_DISABLE);
}

static void report(uint8_t g)
{
	gesture = g;
	exec_trigger(VM_T_TOUCH, g);
}

static void touch_thread(void *a, void *b, void *c)
{
	int64_t press_at = 0, release_at = 0;
	bool pressed = false, held = false;
	int taps = 0;

	gpio_pin_configure_dt(&irq, GPIO_INPUT);
	gpio_init_callback(&irq_cb, irq_isr, BIT(irq.pin));
	gpio_add_callback(irq.port, &irq_cb);
	irq_arm(true);
	k_msleep(60);
	present = bring_up() == 0;
	if (!present) {
		irq_arm(false);
		gpio_pin_configure_dt(&irq, GPIO_DISCONNECTED);
		return;
	}
	active = true;
	exec_wake();
	for (;;) {
		int64_t now, due = -1;
		uint32_t v;

		if (atomic_get(&want_active) != active) {
			active = !active;
			touch_reg_write(REG_CMD, active ? CMD_ACTIVE : CMD_SLEEP);
			irq_arm(active);
			pressed = held = false;
			taps = 0;
			LOG_INF("TouchBar controller %s", active ? "scanning" : "asleep");
		}
		now = k_uptime_get();
		if (pressed) {
			due = held ? press_at + STUCK_MS : press_at + HOLD_MS;
		} else if (taps) {
			due = release_at + TAP_MS;
		}
		bool woke = active && gpio_pin_get_dt(&irq) > 0;

		if (!woke) {
			woke = k_sem_take(&sem, due < 0 ? K_FOREVER : K_MSEC(MAX(due - now, 0))) == 0;
		}

		now = k_uptime_get();
		while ((woke || gpio_pin_get_dt(&irq) > 0) && touch_reg_read(REG_IRQSRC, &v) == 0 && v) {
			woke = false;
			events++;
			if (v & EV_RESET) {
				LOG_WRN("TouchBar controller reset (events 0x%x): configuration loaded again", v);
				load();
			}
			if (v & (EV_WORN | EV_REMOVED)) {
				wear_controller(v & EV_WORN);
				exec_wake();
			}
			if ((v & EV_DOWN) && !pressed && active) {
				taps = now - release_at <= TAP_MS ? taps + 1 : 1;
				press_at = now;
				pressed = true;
				held = false;
			}
			if ((v & EV_UP) && pressed) {
				pressed = false;
				release_at = now;
				if (held) {
					report(5);
					taps = 0;
				}
			}
		}
		if (pressed && !held && now - press_at >= HOLD_MS) {
			held = true;
			taps = 0;
			report(4);
		} else if (pressed && held && now - press_at >= STUCK_MS) {
			pressed = false;
			report(5);
		} else if (!pressed && taps && now - release_at >= TAP_MS) {
			if (taps <= 3) {
				report(taps);
			}
			taps = 0;
		}
	}
}

K_THREAD_DEFINE(touch_tid, 1024, touch_thread, NULL, NULL, NULL, 7, 0, K_TICKS_FOREVER);

void touch_start(void)
{
	atomic_set(&want_active, 1);
	k_thread_start(touch_tid);
}

/* the executor, after each run: the controller scans only while a program or an EVENTS bit wants TOUCH or
 * WEAR (VM.md §6.6) */
void touch_use(bool on)
{
	if (atomic_set(&want_active, on) != on && present) {
		k_sem_give(&sem);
	}
}

bool touch_present(void)
{
	return present;
}

uint32_t touch_prop(uint32_t id)
{
	if (id == VM_P_WORN) {
		return wear_prop();
	}
	return present ? gesture : VM_UNKNOWN;
}

void touch_stats(uint32_t *ev, uint32_t *errors, uint32_t *loaded, uint32_t *interrupts, bool *scanning)
{
	*ev = events;
	*errors = i2c_errors;
	*loaded = loads;
	*interrupts = irqs;
	*scanning = present && active;
}
#else
int touch_reg_read(uint16_t reg, uint32_t *v)
{
	*v = 0xFFFFFFFF;
	return -ENODEV;
}

int touch_reg_write(uint16_t reg, uint32_t v)
{
	return -ENODEV;
}

void touch_start(void)
{
}

void touch_use(bool on)
{
}

bool touch_present(void)
{
	return false;
}

uint32_t touch_prop(uint32_t id)
{
	return id == VM_P_WORN ? wear_prop() : VM_UNKNOWN;
}

void touch_stats(uint32_t *ev, uint32_t *errors, uint32_t *loaded, uint32_t *interrupts, bool *scanning)
{
	*ev = *errors = *loaded = *interrupts = 0;
	*scanning = false;
}
#endif
