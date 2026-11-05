/* nPM1300 (TWIM1, 0x6B). Every write below is a value from the stock 1.5.6 trace
 * (emu/results/npm1300-writes-*.txt, firmware-plan.md §4); the only change is the order: VTERM is
 * decided from the board SN before charging is enabled. Battery temperature (NTC) faults are handled
 * exactly as stock does (user's decision): by the nPM1300's own NTC thresholds, no software action. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include "g1.h"

LOG_MODULE_REGISTER(pmic, LOG_LEVEL_INF);

#define PMIC_ADDR 0x6B
#define VTERM_4V40 0x0C
#define VTERM_4V20 0x08

/* 1 and 2 were NTC open/short, dropped: stock never refuses charging on the NTC reading. */
enum refuse { REFUSE_NONE = 0, REFUSE_VERIFY = 3, REFUSE_I2C = 4 };

struct pmic_status pmic_status;
static const struct device *i2c = DEVICE_DT_GET(DT_NODELABEL(i2c1));
static K_MUTEX_DEFINE(bus);
static K_SEM_DEFINE(ready_sem, 0, 1);
static volatile bool stop_kicks;
static bool charge_allowed;

static const uint8_t ntc_thresholds[8] = {0xEB, 0x01, 0xAD, 0x01, 0x3F, 0x03, 0x2E, 0x02};

/* a transfer that failed three times: the first 8, then every 100th, into the log */
static void failed(uint8_t page, uint8_t off, int err)
{
	static uint32_t n;

	if (n++ < 8 || n % 100 == 0) {
		LOG_WRN("PMIC %02x %02x: I2C error %d (%u transfers failed)", page, off, err, n);
	}
}

static int wr(uint8_t page, uint8_t off, const uint8_t *data, size_t n)
{
	uint8_t buf[8] = {page, off};
	int err = -EIO;

	memcpy(&buf[2], data, n);
	k_mutex_lock(&bus, K_FOREVER);
	for (int i = 0; i < 3 && err; i++) {
		err = i2c_write(i2c, buf, n + 2, PMIC_ADDR);
		if (err) {
			pmic_status.i2c_errors++;
			k_msleep(1);
		}
	}
	k_mutex_unlock(&bus);
	if (err) {
		failed(page, off, err);
	}
	return err;
}

static int wr1(uint8_t page, uint8_t off, uint8_t v)
{
	return wr(page, off, &v, 1);
}

static int rd(uint8_t page, uint8_t off, uint8_t *out, size_t n)
{
	uint8_t a[2] = {page, off};
	int err = -EIO;

	k_mutex_lock(&bus, K_FOREVER);
	for (int i = 0; i < 3 && err; i++) {
		err = i2c_write_read(i2c, PMIC_ADDR, a, 2, out, n);
		if (err) {
			pmic_status.i2c_errors++;
			k_msleep(1);
		}
	}
	k_mutex_unlock(&bus);
	if (err) {
		failed(page, off, err);
	}
	return err;
}

int pmic_i2c_read(uint8_t page, uint8_t off, uint8_t *out, size_t n)
{
	return rd(page, off, out, n);
}

static int kick(void)
{
	return wr1(0x07, 0x04, 0x01);
}

/* Before a deliberate reboot that is not an install: a fresh kick gives the next boot the full 30 s
 * of the nPM1300 watchdog (07 04 01 is in the stock register set). */
void pmic_kick_now(void)
{
	if (pmic_status.present && !stop_kicks) {
		kick();
	}
}

/* Before a deliberate reboot into an install. Stock does this in its DFU mode (decomp
 * low_speed_peripheral_dispatch_thread: wdt_disable = TIMER STOP 07 01 01, up to 5 tries 50 ms apart,
 * then no kicks; the next image's init_watchdog re-arms it), so MCUboot's validate + erase + copy
 * can never be cut by a power cycle. The kick first means that even if STOP were ignored, MCUboot
 * gets a full 30 s. No kicks after this. The PMIC is tried even before the PMIC thread has run, but
 * not once that thread found it absent (each failed transfer takes ~0.5 s on a bare DK). */
void pmic_watchdog_stop_for_install(void)
{
	stop_kicks = true;
	if (!pmic_status.present && pmic_status.i2c_errors) {
		LOG_WRN("no PMIC: no PMIC watchdog to stop");
		return;
	}
	kick();
	for (int i = 0; i < 5; i++) {
		if (wr1(0x07, 0x01, 0x01) == 0) {
			pmic_status.wdt_armed = false;
			LOG_WRN("PMIC watchdog stopped for the install (07 01 01)");
			return;
		}
		k_msleep(50);
	}
	LOG_WRN("PMIC watchdog stop failed (no PMIC?): MCUboot has up to 30 s from the last kick");
}

void pmic_stop_kicking(void)
{
	stop_kicks = true;
}

/* the case link: stock's charger enable after a battery reply with status 0 or 4 (BCHGENABLESET) */
void pmic_charger_enable(void)
{
	if (charge_allowed) {
		pmic_status.charging_enabled = wr1(0x03, 0x04, 0x01) == 0;
	}
}

/* VBUS from the case's NFC charger (nPM1300 VBUSIN status bit 0), as last read */
bool pmic_vbus_present(void)
{
	return pmic_status.vbus_status & 0x01;
}

/* IN_CASE (VM.md §2.5, review-v5.md §10.10): a state, set by the case's power (VBUS, sampled every second), its
 * field (the tag's FIELD_ON) or a case message; cleared 3 s after the last of these if the arm moved more than
 * 150 mg between two samples since then (lifted out of the case) or was classified as worn, or
 * G1_IN_CASE_HOLD_S after the last of them (a full case re-checks about every 300 s). It gates the in-case
 * rules (proto.c). */
static atomic_t in_case_on;
static volatile uint32_t seen_ms;
static atomic_t moved;

void g1_case_seen(void)
{
	seen_ms = k_uptime_get_32();
	atomic_set(&moved, 0);
	atomic_set(&in_case_on, 1);
}

void g1_case_moved(void)
{
	atomic_set(&moved, 1);
}

bool g1_in_case(void)
{
	return atomic_get(&in_case_on);
}

/* DIAG CASE: how long since the last VBUS sample, field or message, and whether the arm moved since */
uint32_t g1_case_since_ms(bool *moved_since)
{
	*moved_since = atomic_get(&moved);
	return k_uptime_get_32() - seen_ms;
}

static void in_case_tick(void)
{
	uint32_t since = k_uptime_get_32() - seen_ms;

	if (pmic_vbus_present()) {
		g1_case_seen();
	} else if (atomic_get(&in_case_on) && ((since >= 3000 && (atomic_get(&moved) || touch_worn())) ||
						since >= CONFIG_G1_IN_CASE_HOLD_S * 1000U)) {
		LOG_INF("IN_CASE 0: %s", touch_worn() ? "worn" : atomic_get(&moved) ? "moved" : "nothing for a while");
		atomic_set(&in_case_on, 0);
	}
}

/* is_old_board() of the stock firmware: chars 4..8 of the board SN = "EGP0x" or "EGP1[0-3]". */
static uint8_t choose_vterm(void)
{
	const uint8_t *sn = g1_settings.sn;

	for (int i = 0; i < 9; i++) {
		if (sn[i] < 0x21 || sn[i] > 0x7E) {
			LOG_WRN("board SN unreadable: VTERM 4.20 V");
			return VTERM_4V20;
		}
	}
	if (sn[4] == 'E' && sn[5] == 'G' && sn[6] == 'P' &&
	    (sn[7] == '0' || (sn[7] == '1' && sn[8] >= '0' && sn[8] <= '3'))) {
		LOG_INF("old board SN %.9s: VTERM 4.20 V", sn);
		return VTERM_4V20;
	}
	LOG_INF("new board SN %.9s: VTERM 4.40 V", sn);
	return VTERM_4V40;
}

/* One measurement round (every second): charger and VBUS state, then the ADC tasks NTC, die
 * temperature, VSYS, VBUS and VBAT (which is followed by IBAT, ADCIBATMEASEN = 1 in configure()).
 * Stock triggers NTC and VBAT the same way (05 01 01, 05 00 01); the other three are measurement tasks
 * only. Results 0x10..0x1A as the NCS npm1300_charger driver reads them. */
/* one VBAT conversion and the IBAT the PMIC takes after it (ADCIBATMEASEN, as stock), read wait_us later */
static int convert(uint32_t wait_us, struct adc_conv *c)
{
	uint8_t r[11];
	int err = wr1(0x05, 0x00, 0x01);

	if (!err) {
		k_usleep(wait_us);
		err = rd(0x05, 0x10, r, sizeof(r));
	}
	if (err) {
		return err;
	}
	c->t_us = k_cyc_to_us_floor32(k_cycle_get_32());
	c->mv = ((r[1] << 2) | (r[5] & 3)) * 5000 / 1024;
	if (!power_ibat_code_ua(r[0], (r[8] << 2) | ((r[10] >> 4) & 3), &c->ua)) {
		c->ua = INT32_MIN;
	}
	return 0;
}

/* While the panel is on, each round adds BURST_N conversions at unequal spacings over CONFIG_G1_GUARD_BURST_MS or
 * more, so that the panel's pulsed current averages out (no pulse period aliases to one phase): their mean IBAT and
 * their lowest VBAT go to the panel guard (VM.md §6.6). */
#define BURST_N 16
#define BURST_WAIT_US MAX(2000, CONFIG_G1_GUARD_BURST_MS * 1000 / BURST_N - 900)
static const uint16_t jitter_us[BURST_N] = {0, 600, 200, 1000, 400, 800, 100, 700, 300, 900, 500, 50, 650, 250, 950, 450};
static struct {
	int32_t ua;
	uint16_t mv_min;
	uint8_t n;
	bool fresh;
} burst_out;

static void burst(void)
{
	struct adc_conv c;
	int64_t sum = 0;
	uint16_t low = UINT16_MAX;
	uint8_t n = 0;

	k_mutex_lock(&bus, K_FOREVER);
	for (int i = 0; i < BURST_N && convert(BURST_WAIT_US + jitter_us[i], &c) == 0; i++) {
		low = MIN(low, c.mv);
		if (c.ua != INT32_MIN) {
			sum += c.ua;
			n++;
		}
	}
	k_mutex_unlock(&bus);
	burst_out.ua = n ? (int32_t)(sum / n) : 0;
	burst_out.mv_min = low;
	burst_out.n = n;
	burst_out.fresh = n > 0;
}

bool pmic_burst(int32_t *ua, uint16_t *mv_min, uint8_t *n)
{
	*ua = burst_out.ua;
	*mv_min = burst_out.mv_min;
	*n = burst_out.n;
	return burst_out.fresh;
}

/* DIAG ADC_BURST: n conversions wait_us apart, the bus held meanwhile */
int pmic_adc_record(uint8_t n, uint16_t wait_us, struct adc_conv *out)
{
	int err = 0;

	k_mutex_lock(&bus, K_FOREVER);
	for (int i = 0; i < n && !err; i++) {
		err = convert(MAX(wait_us, 300), &out[i]);
	}
	k_mutex_unlock(&bus);
	return err;
}

/* false when it could not read a new sample */
static bool sample(void)
{
	static struct pmic_sample m;
	uint8_t r[11], v;

	burst_out.fresh = false;
	if (rd(0x03, 0x34, &v, 1) != 0) {
		return false;   /* the bus fails: keep the loop short, the watchdog kick comes first */
	}
	pmic_status.charge_status = v;
	if (rd(0x03, 0x36, &v, 1) == 0) {
		m.charge_error = v;
	}
	if (rd(0x08, 0x04, &v, 1) == 0) {
		m.ldsw_status = v;
	}
	wr1(0x05, 0x01, 0x01);
	wr1(0x05, 0x02, 0x01);
	wr1(0x05, 0x03, 0x01);
	wr1(0x05, 0x07, 0x01);
	wr1(0x05, 0x00, 0x01);
	k_msleep(20);
	if (rd(0x05, 0x10, r, sizeof(r)) != 0) {
		return false;
	}
	m.ibat_stat = r[0];
	m.vbat_code = (r[1] << 2) | (r[5] & 3);
	m.ntc_code = (r[2] << 2) | ((r[5] >> 2) & 3);
	m.die_code = (r[3] << 2) | ((r[5] >> 4) & 3);
	m.vsys_code = (r[4] << 2) | ((r[5] >> 6) & 3);
	m.ibat_code = (r[8] << 2) | ((r[10] >> 4) & 3);
	m.vbus_code = (r[9] << 2) | ((r[10] >> 6) & 3);
	m.charge_status = pmic_status.charge_status;
	m.vbus_status = pmic_status.vbus_status;
	pmic_status.vbat_mv = m.vbat_code * 5000 / 1024;
	pmic_status.ntc_code = m.ntc_code;
	power_sample(&m);
	if (panel_is_on()) {
		burst();
	}
	return true;
}

/* The nPM1300's own record of why it last reset the system (product specification, "Reset and error registers",
 * ERRLOG at 0x0E00): RSTCAUSE bit 0 ship mode exit, 1 boot monitor, 2 watchdog, 3 long press, 4 thermal shutdown,
 * 5 VSYS low, 6 software reset; then the charger's error reason and sensor. Read at boot, cleared by TASKCLRERRLOG
 * when set, so each boot logs only what happened since the last one. */
static void reset_cause(void)
{
	uint8_t c[3];

	for (int i = 0; i < 3; i++) {
		if (rd(0x0E, 0x03 + i, &c[i], 1) != 0) {
			return;
		}
	}
	LOG_INF("PMIC reset cause 0x%02x, charger error 0x%02x, sensor 0x%02x", c[0], c[1], c[2]);
	if (c[0] || c[1] || c[2]) {
		wr1(0x0E, 0x00, 0x01);
	}
}

static int configure(void)
{
	uint8_t v, check[8];
	int err = 0;

	/* GPIO2 = reset output, as the stock driver init */
	err |= wr1(0x06, 0x02, 0x06);
	err |= wr1(0x06, 0x16, 0x00);
	err |= wr1(0x06, 0x0C, 0x00);
	err |= wr1(0x06, 0x11, 0x00);
	err |= wr1(0x06, 0x07, 0x00);
	err |= wr1(0x06, 0x1B, 0x00);
	/* BUCK2 1.2 V under software control (BUCK1 untouched), LDSW1/2 as load switches */
	err |= rd(0x04, 0x0F, &v, 1);
	err |= rd(0x04, 0x10, &v, 1);
	err |= wr1(0x04, 0x0A, 0x02);
	err |= rd(0x04, 0x0F, &v, 1);
	err |= wr1(0x04, 0x0F, v | 0x02);
	err |= wr1(0x08, 0x08, 0x00);
	err |= rd(0x08, 0x0C, &v, 1);
	err |= wr1(0x08, 0x09, 0x00);
	err |= rd(0x08, 0x0D, &v, 1);
	/* charger, all values as stock; VTERM chosen before enabling */
	pmic_status.vterm_code = choose_vterm();
	err |= wr1(0x05, 0x0A, 0x03);
	err |= wr1(0x03, 0x0C, pmic_status.vterm_code);
	err |= wr1(0x03, 0x0D, 0x06);
	err |= wr(0x03, 0x08, (const uint8_t[]){0x09, 0x01}, 2);
	err |= wr(0x03, 0x0A, (const uint8_t[]){0x9A, 0x01}, 2);
	err |= wr1(0x02, 0x02, 0x05);
	err |= wr1(0x03, 0x0E, 0x00);
	err |= wr1(0x03, 0x0F, 0x00);
	err |= wr1(0x05, 0x24, 0x01);
	err |= wr1(0x05, 0x00, 0x01);
	err |= wr1(0x05, 0x01, 0x01);
	err |= wr1(0x05, 0x0C, 0x01);
	for (int i = 0; i < 8; i++) {
		err |= wr1(0x03, 0x10 + i, ntc_thresholds[i]);
	}
	if (err) {
		pmic_status.refuse_reason = REFUSE_I2C;
		return -EIO;
	}
	if (rd(0x03, 0x0C, &v, 1) || v != pmic_status.vterm_code || rd(0x03, 0x10, check, 8) ||
	    memcmp(check, ntc_thresholds, 8)) {
		pmic_status.refuse_reason = REFUSE_VERIFY;
		LOG_ERR("charger read-back mismatch: charging stays off");
		return 0;
	}
	sample();
	charge_allowed = true;
	pmic_status.charging_enabled = wr1(0x03, 0x04, 0x01) == 0;
	LOG_INF("charging enabled, VTERM code 0x%02x, NTC %u", pmic_status.vterm_code, pmic_status.ntc_code);
	return 0;
}

static void arm_watchdog(void)
{
	int err = 0;

	err |= wr(0x07, 0x08, (const uint8_t[]){0x00, 0x07, 0x53}, 3);
	err |= wr1(0x07, 0x03, 0x01);
	err |= wr1(0x07, 0x05, 0x02);
	err |= wr1(0x07, 0x00, 0x01);
	err |= kick();
	pmic_status.wdt_armed = err == 0;
	LOG_INF("PMIC watchdog %s (30 s, power cycle)", err ? "NOT armed" : "armed");
}

/* LDSW2 (IMU and microphone; stock turns it on at every boot, and so does g1v0 since 0.4.8). */
int pmic_ldsw2_on(void)
{
	if (!pmic_status.configured) {
		return -ENODEV;
	}
	return wr1(0x08, 0x02, 0x01) ? -EIO : 0;
}

/* LDSW2 off: only for a power measurement (diag.c); the IMU is gone until it is on again */
int pmic_ldsw2_off(void)
{
	if (!pmic_status.configured) {
		return -ENODEV;
	}
	return wr1(0x08, 0x03, 0x01) ? -EIO : 0;
}

int pmic_panel_rails(bool on)
{
	uint8_t v;
	int err = 0;

	if (!pmic_status.configured) {
		return -ENODEV;
	}
	if (on) {
		err |= wr1(0x08, 0x00, 0x01);
		k_msleep(1);
		err |= wr1(0x04, 0x0A, 0x02);
		err |= rd(0x04, 0x0F, &v, 1);
		err |= wr1(0x04, 0x0F, v | 0x02);
		err |= wr1(0x04, 0x02, 0x01);
		k_msleep(1);
		board_panel_enable(true);
		k_msleep(6);
	} else {
		board_panel_enable(false);
		k_msleep(16);
		err |= wr1(0x04, 0x03, 0x01);
		k_msleep(5);
		err |= wr1(0x08, 0x01, 0x01);
	}
	return err ? -EIO : 0;
}

bool pmic_wait_ready(k_timeout_t timeout)
{
	if (k_sem_take(&ready_sem, timeout) == 0) {
		k_sem_give(&ready_sem);
	}
	return pmic_status.configured;
}

/* The nPM1300 watchdog power-cycles the arm 30 s after the last kick, which loses the retained log: a failed kick
 * is logged with the state of the bus's other users, a second failure in a row recovers the bus first, and 20 s
 * without a good kick end in a warm reboot (health reason 7) that keeps the log. A kick counts once NTCCOLD reads
 * back as it read after configure(): while a device holds SDA low every byte is ACKed and reads 0, so a kick can
 * succeed without reaching the PMIC. */
static uint32_t kick_ok_ms, kick_try_ms, kick_fails;
static uint8_t check_v;

static int kick_checked(uint8_t *v)
{
	int err = kick();

	*v = 0;
	if (!err && check_v) {
		err = rd(0x03, 0x10, v, 1);
		if (!err && *v != check_v) {
			err = -EBADMSG;
		}
	}
	return err;
}

/* Zephyr's TWIM recovery does not wait for a transfer in progress: the PMIC's own users at least */
static void recover(void)
{
	k_mutex_lock(&bus, K_FOREVER);
	i2c_recover_bus(i2c);
	k_mutex_unlock(&bus);
}

static void watchdog_tick(void)
{
	uint32_t now = k_uptime_get_32(), n, e;
	uint8_t v;
	bool on;
	int err;

	if (now - kick_try_ms < 5000 || !health_good() || stop_kicks) {
		return;
	}
	kick_try_ms = now;
	err = kick_checked(&v);
	if (err && kick_fails) {
		recover();
		err = kick_checked(&v);
	}
	if (!err) {
		if (kick_fails) {
			LOG_WRN("PMIC kick works again after %u failures", kick_fails);
		}
		kick_fails = 0;
		kick_ok_ms = now;
		return;
	}
	kick_fails++;
	light_stats(&n, &e, &on);
	LOG_WRN("PMIC kick failed (%d, check 0x%02x), the last good one %u ms ago; I2C errors %u; panel %u, standby %u; "
		"light %u, %u errors", err, v, now - kick_ok_ms, pmic_status.i2c_errors, panel_is_on(), panel_in_standby(),
		on, e);
	if (now - kick_ok_ms >= 20000) {
		LOG_ERR("PMIC unreachable for %u ms: warm reboot before its watchdog cuts the power", now - kick_ok_ms);
		retained_set_health_reason(7);
		g1_reboot(CLEAN_NONE, false, false);
	}
}

/* while the panel is on, every 10 s: VBAT and the lowest VBAT of the rounds' bursts since the last line, IBAT, the
 * last burst's mean IBAT and the panel guard's average, its cap and the level shown, the light sensor */
static void power_line(uint32_t t)
{
	static uint16_t low = UINT16_MAX;
	uint32_t n, e;
	int32_t ua;
	uint16_t mv;
	uint8_t k;
	bool on;

	if (!panel_is_on()) {
		low = UINT16_MAX;
		return;
	}
	low = MIN(low, pmic_burst(&ua, &mv, &k) ? mv : pmic_status.vbat_mv);
	if (t % 10 == 0) {
		light_stats(&n, &e, &on);
		LOG_INF("power: %u mV (lowest %u), %d mA (burst %d of %u, average %d); cap %u, level %u; light %u, "
			"%u readings, %u errors", pmic_status.vbat_mv, low, power_ibat_ua() / 1000, ua / 1000, k,
			guard_avg_ua() / 1000, panel_cap(), panel_effective_level(), on, n, e);
		low = UINT16_MAX;
	}
}

static void pmic_thread(void *a, void *b, void *c)
{
	uint8_t s[2];

	/* the previous image may have armed the watchdog: kick first */
	while (kick() != 0) {
		atomic_inc(&hb_pmic);
		LOG_ERR("PMIC not answering on I2C (errors %u)", pmic_status.i2c_errors);
		k_sem_give(&ready_sem);
		recover();
		k_sleep(K_SECONDS(5));
	}
	kick_ok_ms = kick_try_ms = k_uptime_get_32();
	pmic_status.present = true;
	reset_cause();
	pmic_status.configured = configure() == 0;
	if (rd(0x03, 0x10, &check_v, 1) != 0) {
		check_v = 0;
	}
	arm_watchdog();
	k_sem_give(&ready_sem);
	/* stock's power_for_imu_and_mic(): LDSW2 on at every boot; then the IMU's boot state (imu.c) */
	if (pmic_status.configured && pmic_ldsw2_on() == 0) {
		k_msleep(20);
	}
	imu_init();
	/* VBUS (the in-case signal) and one ADC sample every second; the watchdog kick every 5 s (watchdog_tick) */
	bool in_case = false;

	for (uint32_t t = 0;; t++) {
		atomic_inc(&hb_pmic);
		watchdog_tick();
		if (rd(0x02, 0x07, s, 1) == 0) {
			bool was = pmic_vbus_present();

			pmic_status.vbus_status = s[0];
			if (pmic_vbus_present() != was) {
				LOG_DBG("VBUS %s", was ? "lost" : "present");
				caselink_vbus_changed();
			}
		}
		in_case_tick();
		if (g1_in_case() != in_case) {
			in_case = !in_case;
			LOG_INF("%s the case", in_case ? "in" : "out of");
			if (in_case) {
				touch_case_entered();
			}
			proto_vbus_changed();
			caselink_vbus_changed();
		}
		guard_tick(sample());
		power_line(t);
		caselink_battery_tick();
		exec_sensors();
		k_sleep(K_SECONDS(1));
	}
}

K_THREAD_DEFINE(pmic, 1536, pmic_thread, NULL, NULL, NULL, 6, 0, K_TICKS_FOREVER);

void pmic_start(void)
{
	k_thread_start(pmic);
}
