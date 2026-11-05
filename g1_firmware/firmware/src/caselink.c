/* Case link: the charging case's requests over the ST25DV mailbox (TWIM1, 0x53), answered as stock 1.6.6
 * does (docs/power.md §4; decomp process_pt_data, process_pt_protocol, get_battery_info, comm_write).
 * The case writes a message (e.g. 13 00 01 00 64 07: battery info, case battery 100 %, lid open); the
 * tag pulses its GPO (P1.10, stock's nfc_gpo_init pin); the arm reads IT_STS_Dyn, MB_CTRL_Dyn, MB_LEN_Dyn
 * and the message, and puts its reply in the mailbox, which the case reads over RF. Stock's reply to
 * 13 tells the case whether to keep powering the arm: status 0 charging, 1 full, 2 battery too warm,
 * 4 powered but not charging. Without a reply the case logs "read rx info timeout, goto charge" and
 * keeps its NFC power transfer on for good. VRECT is measured as stock: SAADC AIN3 (P0.07), gain 1/6,
 * internal reference, 12 bit, x10. Requests other than 13, 06 (lid closed / long trip) and 0B (restart)
 * get stock's reply for an unknown command; 06 "long trip" puts stock into ship mode and 0B reboots it, which
 * g1v0 only logs (VM.md §6.8; ship mode is the user's decision). As stock, the charger is enabled after each
 * battery reply with status 0 (charging) or 4 (powered, not charging). */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/adc.h>
#include <zephyr/drivers/gpio.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include <hal/nrf_saadc.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(caselink, LOG_LEVEL_INF);

#define TAG 0x53
#define TAG_SYS 0x57
#define ST_EH_CTRL 0x2002
#define FIELD_ON 0x04
#define ST_IT_STS 0x2005
#define ST_MB_CTRL 0x2006
#define ST_MB_LEN 0x2007
#define ST_MB 0x2008

struct caselink_state caselink;

#if IS_ENABLED(CONFIG_G1_CASE_LINK)
static const struct device *bus = DEVICE_DT_GET(DT_NODELABEL(i2c1));
static const struct device *adc = DEVICE_DT_GET(DT_NODELABEL(adc));
static const struct gpio_dt_spec gpo = GPIO_DT_SPEC_GET(DT_PATH(zephyr_user), nfc_gpo_gpios);
static struct gpio_callback gpo_cb;
static K_SEM_DEFINE(wake, 0, 1);
static int16_t vrect_raw;
static uint8_t reply[16];
/* stock's get_battery_info state: full flag, the two poll counters, the time of the last 0x80 */
static uint8_t full, cnt_a, cnt_b;
static uint32_t last_80_ms;

static int rd(uint16_t reg, uint8_t *out, size_t n)
{
	uint8_t a[2] = {reg >> 8, reg & 0xFF};
	int err = -EIO;

	for (int i = 0; i < 3 && err; i++) {
		err = i2c_write_read(bus, TAG, a, 2, out, n);
		if (err) {
			caselink.i2c_errors++;
			k_msleep(1);
		}
	}
	return err;
}

static int wr(uint16_t reg, const uint8_t *data, size_t n)
{
	uint8_t b[2 + sizeof(reply)] = {reg >> 8, reg & 0xFF};
	int err = -EIO;

	memcpy(&b[2], data, n);
	for (int i = 0; i < 3 && err; i++) {
		err = i2c_write(bus, b, n + 2, TAG);
		if (err) {
			caselink.i2c_errors++;
			k_msleep(1);
		}
	}
	return err;
}

static volatile uint32_t gpo_us;

/* DIAG CASE_TRACE: the last events of the case link, ms of uptime and what */
static struct {
	struct caselink_event e[32];
	uint32_t n;
} trace;

static void note(uint8_t kind, uint8_t value)
{
	struct caselink_event *x = &trace.e[trace.n++ % ARRAY_SIZE(trace.e)];

	x->ms = k_uptime_get_32();
	x->kind = kind;
	x->value = value;
}

size_t caselink_trace(uint8_t *out, size_t max)
{
	size_t k = MIN(trace.n, MIN(ARRAY_SIZE(trace.e), max / 6));
	uint32_t first = trace.n - k;

	for (size_t i = 0; i < k; i++) {
		const struct caselink_event *x = &trace.e[(first + i) % ARRAY_SIZE(trace.e)];

		put32(&out[6 * i], x->ms);
		out[6 * i + 4] = x->kind;
		out[6 * i + 5] = x->value;
	}
	return 6 * k;
}

static void gpo_isr(const struct device *d, struct gpio_callback *cb, uint32_t pins)
{
	gpo_us = now_us();
	caselink.gpo_events++;
	k_sem_give(&wake);
}

/* how long the arm took from the case's message (its GPO pulse, else the look that found it) to the reply in
 * the mailbox; the case reads the mailbox 75 ms after it wrote (DIAG CASE) */
static void reply_time(uint32_t t0)
{
	uint32_t dt = now_us() - t0;

	caselink.reply_n++;
	caselink.reply_sum_us += dt;
	caselink.reply_max_us = MAX(caselink.reply_max_us, dt);
	if (dt > 50000) {
		caselink.reply_late++;
	}
}

static const struct adc_channel_cfg vrect_cfg = {
	.gain = ADC_GAIN_1_6,
	.reference = ADC_REF_INTERNAL,
	.acquisition_time = ADC_ACQ_TIME_DEFAULT,
	.channel_id = 3,
	.input_positive = NRF_SAADC_INPUT_AIN3,
};

/* stock's adc_nfc_run() x 10: mV at AIN3 = raw x 3600 / 4096, valid 1..1199, VRECT = 10 x that */
static uint16_t vrect_mv(void)
{
	struct adc_sequence seq = {
		.channels = BIT(3),
		.buffer = &vrect_raw,
		.buffer_size = sizeof(vrect_raw),
		.resolution = 12,
	};
	int32_t mv;

	if (adc_read(adc, &seq) != 0) {
		return 0;
	}
	mv = (int32_t)vrect_raw * 3600 / 4096;
	return (mv >= 1 && mv <= 1199) ? mv * 10 : 0;
}

/* Battery level for the case: 100 once the charger has completed, latched until the battery, not charging,
 * is 100 mV below VTERM (stock: full until its fuel gauge drops below 99 %; a voltage needs this margin
 * for the relaxation after charging), else the BATTERY estimate clamped to 99 (power.md §4.3). The case's
 * re-check every ~5 min powers the arm for a moment and starts the charger: that must not end "full",
 * or the case runs a whole top-up each time (seen on 0.4.8). */
static uint16_t vterm_mv(void)
{
	return pmic_status.vterm_code ? 3800 + 50 * pmic_status.vterm_code : 4200;
}

/* every second from the PMIC thread, so a battery used out of the case (no requests) ends "full" then.
 * The latch survives soft resets (retained RAM), so a reboot in the full case costs no top-up; it ends
 * only after 30 s in a row below the margin, since the boot's own load (the panel check) dips VBAT for
 * a moment (0.5.4 lost it at every reboot that way). */
void caselink_battery_tick(void)
{
	static bool loaded;
	static uint8_t low;
	uint8_t cs = pmic_status.charge_status;
	uint16_t mv = pmic_status.vbat_mv;

	if (!loaded) {
		loaded = true;
		caselink.full_latched = retained_case_full();
	}
	if ((cs & 0x02) && pmic_vbus_present()) {
		caselink.full_latched = true;
		low = 0;
	} else if (!(cs & 0x1C) && mv && mv < vterm_mv() - 100) {
		if (++low >= 30) {
			caselink.full_latched = false;
		}
	} else {
		low = 0;
	}
	if (caselink.full_latched != retained_case_full()) {
		retained_set_case_full(caselink.full_latched);
	}
}

/* DIAG SET 8 (test builds): the latch as if the charger had completed (1) or the battery had run low (0: the next
 * poll asks the case to charge) */
void caselink_set_full(bool on)
{
	caselink.full_latched = on;
	retained_set_case_full(on);
	if (!on) {
		full = 0;
	}
}

static uint8_t level(void)
{
	uint16_t vterm = vterm_mv();
	uint16_t mv = pmic_status.vbat_mv;

	if (caselink.full_latched) {
		return 100;
	}
	if (!pmic_status.present || !mv || vterm <= 3600) {
		return 0;
	}
	return CLAMP(((int)mv - 3500) * 100 / (vterm - 3600), 0, 99);
}

/* stock 1.6.6 get_battery_info (0x33954), field by field: request 13 00 nn 00 box_battery lid */
static size_t battery_info(const uint8_t *q, size_t qn, uint8_t *r)
{
	uint32_t now = k_uptime_get_32();
	uint16_t vrect = vrect_mv();
	int32_t ma = power_ibat_ua() / 1000;      /* stock: (int8) mA, negative while charging */
	int16_t t = power_batt_temp();             /* 0.1 degC; stock compares T x 100 */
	uint8_t vb = MAX((int)pmic_status.vbat_mv / 10 - 200, 0);   /* stock: V x 100 - 200, >= 0 */
	uint8_t soc = level();
	bool chg_ok = pmic_status.charging_enabled;
	bool charging_flag;

	if (qn > 4) {
		caselink.box_battery = q[4];
		caselink.box_known = true;
		if (qn != 5) {
			caselink.lid = q[5];
			if (q[5] == 7) {
				caselink.lid_closed = false;
				caselink.lid_known = true;
			} else if (q[5] == 8 || q[5] == 6) {
				caselink.lid_closed = true;
				caselink.lid_known = true;
			}
		}
	}
	ma = CLAMP(ma, -127, 127);
	r[0] = 0x13;
	r[1] = 1;
	r[2] = 2;
	r[3] = 6;
	r[4] = 0;
	if (vrect <= 5499 || ma < 0 || full) {
		cnt_a = 0;
		cnt_b = 0;
	} else if (cnt_a <= 4) {
		cnt_a++;
	}
	if (t != INT16_MIN && (t >= 600 || (t >= 500 && vb > 209))) {
		r[4] = 2;
	}
	if (soc >= 99 && (soc >= 100 || full)) {
		if (soc >= 100) {
			full = 1;
		}
		r[4] = 1;
		cnt_a = 0;
		cnt_b = 0;
	} else {
		if (soc < 99) {
			full = 0;
		}
		if (r[4] != 0 || chg_ok || full) {
			cnt_b = 0;
		} else if (++cnt_b > 39) {
			r[4] = 4;
		}
	}
	r[5] = vrect >> 8;
	r[6] = vrect & 0xFF;
	r[7] = soc;
	if (ma < 0) {
		cnt_a = 0;
		cnt_b = 0;
		r[8] = 1;
	} else {
		r[8] = 0;
	}
	if (vrect >= 5000 || vrect == 0 || ma < 0 || full) {
		r[9] = ma < 0 ? -ma : ma;
	} else if (cnt_a > 39) {
		r[9] = 100;
		r[4] = 4;
	}
	if (r[4] == 0 && full) {
		cnt_a = 0;
		cnt_b = 0;
		r[4] = 1;
	}
	charging_flag = r[4] == 0;
	if ((cnt_a | cnt_b) > 3 && soc > 2 && soc <= 99 && now - last_80_ms > 10000) {
		last_80_ms = now;
		r[9] = 0x80;
	}
	caselink.status = r[4];
	caselink.level = soc;
	caselink.vrect_mv = vrect;
	caselink.charging = charging_flag;
	return 10;
}

static void put(const uint8_t *r, size_t n)
{
	uint8_t c;

	/* comm_write: only into a free mailbox (no host or RF message pending) */
	if (rd(ST_MB_CTRL, &c, 1) != 0 || (c & 0x06)) {
		caselink.busy++;
		return;
	}
	if (wr(ST_MB, r, n) == 0) {
		caselink.replies++;
	}
	rd(ST_MB_CTRL, &c, 1);
}

static void handle(const uint8_t *q, size_t qn, uint32_t t0)
{
	uint8_t *r = reply;
	size_t n;
	uint8_t sum = 0;

	switch (q[0]) {
	case 0x13:
		caselink.requests++;
		caselink.last_ms = k_uptime_get_32();
		n = battery_info(q, qn, r);
		break;
	case 0x06:   /* into_long_trip_mode: 06 01 (2 lid closed | 3 long trip) 01 00 */
		r[0] = 0x06;
		r[1] = 1;
		r[2] = (qn > 2 && q[2] == 2) ? 2 : 3;
		r[3] = 1;
		r[4] = 0;
		n = 5;
		if (r[2] == 2) {
			caselink.lid_closed = true;
			caselink.lid_known = true;
		} else {
			caselink.long_trip++;
			LOG_WRN("case asked for long-trip (ship) mode: not entered");
		}
		break;
	case 0x0B:   /* PTP_DEVICE_RESTART: 0B 01 03 01 00, then stock reboots after 300 ms */
		r[0] = 0x0B;
		r[1] = 1;
		r[2] = 3;
		r[3] = 1;
		r[4] = 0;
		n = 5;
		break;
	default:     /* process_pt_protocol, command not in its table */
		r[0] = q[0];
		r[1] = 1;
		r[2] = 3;
		r[3] = 1;
		r[4] = 2;
		n = 5;
		caselink.unknown++;
		break;
	}
	for (size_t i = 0; i < n; i++) {
		sum += r[i];
	}
	r[n] = sum;
	put(r, n + 1);
	note(CE_REPLY, q[0] == 0x13 ? r[4] : 0xFF);
	if (q[0] == 0x13) {
		reply_time(t0);
	}
	if (q[0] == 0x13 && r[9] == 0x80) {
		/* stock: 1 s later the same message again with byte 9 = 0 (and the first checksum) */
		k_msleep(1000);
		r[9] = 0;
		put(r, n + 1);
	}
	/* stock (process_pt_data): the charger is enabled after a battery reply with status 0 or 4 */
	if (q[0] == 0x13 && !(r[4] & 0xFB)) {
		pmic_charger_enable();
		caselink.charger_enables++;
	}
	if (q[0] == 0x0B) {
		caselink.restarts++;
		LOG_WRN("case asked for a restart: not obeyed (VM.md §6.8)");
	}
}

/* In the case lit pixels load the case's NFC power until its messages fail (power.md §15): while the panel is on
 * there the LEDs are off (panel_blank, the image kept) from the case's first RF command until QUIET_MS after its last,
 * at a full arm's re-checks and through a top-up (its polls ~650 ms apart). Whether the panel is on is the program's
 * choice (VM.md §6.6). */
#define QUIET_MS 2000

static int64_t pulse_ms = -QUIET_MS, blank_since;

static bool blank_wanted(int64_t now)
{
	return g1_in_case() && !exec_mic_on() && now - pulse_ms < QUIET_MS;
}

/* ms until the next look at the blanking */
static int64_t window_step(int64_t now)
{
	bool want = blank_wanted(now);

	if (want != caselink.blanked) {
		caselink.blanked = want;
		panel_blank(want);
		note(want ? CE_BLANK : CE_UNBLANK, pmic_vbus_present());
		if (want) {
			caselink.blanks++;
			caselink.blank_pulses += !pmic_vbus_present();
			blank_since = now;
		} else {
			caselink.blanked_ms += now - blank_since;
		}
	}
	return caselink.blanked ? 200 : 1000;
}

/* the case's RF field (VM.md §2.5): it keeps IN_CASE set while the case powers the tag */
static void field(void)
{
	uint8_t eh;

	if (rd(ST_EH_CTRL, &eh, 1) == 0) {
		caselink.field = (eh & FIELD_ON) != 0;
		if (caselink.field) {
			caselink.field_ms = k_uptime_get_32();
			g1_case_seen();
		}
	}
}

/* the tag's interrupt bits, then the case's messages if RF_PUT_MSG says one came (every look: a lost pulse) and the
 * field if it changed (every look) */
static void mailbox(uint32_t t0, bool pulsed, bool look)
{
	uint8_t it, c, len, q[32];

	if (rd(ST_IT_STS, &it, 1) != 0) {
		return;
	}
	caselink.it_sts |= it;
	for (int b = 0; b < 8; b++) {
		if (it & BIT(b)) {
			caselink.it_count[b]++;
		}
	}
	if (it) {
		note(CE_IT_STS, it);
	}
	if (look || (it & (BIT(3) | BIT(4)))) {
		field();
	}
	for (int k = 0; k < 4 && (look || (it & BIT(5))); k++) {
		if (rd(ST_MB_CTRL, &c, 1) != 0 || (c & 0x05) != 0x05) {
			break;   /* mailbox off, or no RF message waiting */
		}
		if (rd(ST_MB_LEN, &len, 1) != 0) {
			break;
		}
		size_t n = MIN((size_t)len + 1, sizeof(q));
		if (rd(ST_MB, q, n) != 0) {
			break;
		}
		caselink.messages++;
		caselink.msg_ms = k_uptime_get_32();
		if (!pulsed) {
			caselink.by_look++;
		}
		g1_case_seen();
		handle(q, n, t0);
		exec_wake();
	}
}

/* A pulse marks the start of the case's RF command (or a message or field event): the tag answers I2C only after it,
 * and a read that overlaps the case's next command makes that one fail, so the arm waits 2 ms and reads as little as
 * the interrupt bits ask for (power.md §15). The mailbox first, so a reply never waits for the panel (blanking takes
 * the panel's lock). */
static void service(bool look)
{
	static uint32_t edges;
	uint32_t t0 = now_us();
	bool pulsed = caselink.gpo_events != edges;

	edges = caselink.gpo_events;
	if (pulsed && t0 - gpo_us < 1000000) {
		t0 = gpo_us;
		k_msleep(2);
	}
	mailbox(t0, pulsed, look || !pulsed);
	if (pulsed) {
		pulse_ms = k_uptime_get();
		note(CE_EDGE, 0);
		window_step(pulse_ms);
	}
}

/* CASE_LID, CASE_CHARGING, CASE_BATT_PCT (VM.md §6.8) from the case's last messages, UNKNOWN 3 s after
 * the last one (stock: 3 s without a message = out of the box). The case's battery byte: bits 0-6 the
 * level in %, bit 7 seen set while the case charged on USB (case-serial.md §10; to be confirmed, M9). */
bool caselink_heard(uint32_t ms)
{
	return caselink.messages && k_uptime_get_32() - caselink.msg_ms <= ms;
}

uint32_t caselink_prop(uint32_t id)
{
	bool fresh = caselink_heard(3000);

	switch (id) {
	case VM_P_CASE_LID:
		return fresh && caselink.lid_known ? caselink.lid_closed : VM_UNKNOWN;
	case VM_P_CASE_CHARGING:
		return fresh && caselink.box_known ? caselink.box_battery >> 7 : VM_UNKNOWN;
	case VM_P_CASE_BATT_PCT:
		return fresh && caselink.box_known ? caselink.box_battery & 0x7F : VM_UNKNOWN;
	}
	return VM_UNKNOWN;
}

/* The GPO pin only in the case: the case's RF field (VBUS) is what makes the tag pulse. Out of it the pin
 * is disconnected: an edge interrupt holds a GPIOTE IN channel, and the tag can hold its open-drain GPO
 * low (seen on both arms), so the pull-up alone would draw ~0.14 mA. The 1 s look at the mailbox catches
 * a pulse that comes before the PMIC thread sees VBUS. */
static bool gpo_pullup_out;

static void gpo_irq(bool on)
{
	static int state = -1;
	int want = on ? 2 : gpo_pullup_out ? 1 : 0;

	if (state != want) {
		state = want;
		gpio_pin_interrupt_configure_dt(&gpo, GPIO_INT_DISABLE);
		gpio_pin_configure_dt(&gpo, want ? GPIO_INPUT : GPIO_DISCONNECTED);
		if (want == 2) {
			gpio_pin_interrupt_configure_dt(&gpo, GPIO_INT_EDGE_TO_ACTIVE);
		}
	}
}

static void caselink_thread(void *a, void *b, void *c)
{
	uint8_t gpo_cfg = 0xFF;
	uint8_t reg[2] = {0x00, 0x00};

	pmic_wait_ready(K_SECONDS(10));
	if (device_is_ready(adc)) {
		adc_channel_setup(adc, &vrect_cfg);
	}
	gpio_init_callback(&gpo_cb, gpo_isr, BIT(gpo.pin));
	gpio_add_callback(gpo.port, &gpo_cb);
	i2c_write_read(bus, TAG_SYS, reg, 2, &gpo_cfg, 1);
	caselink.gpo_config = gpo_cfg;
	LOG_INF("case link: ST25DV GPO config 0x%02x", gpo_cfg);
	for (int64_t look = 0;;) {
		bool in_case = g1_in_case();
		int64_t every = in_case ? 1000 : 30000, now = k_uptime_get();
		int64_t ms = MIN(window_step(now), look + every - now);

		gpo_irq(in_case);
		/* the GPO pulse wakes it; in the case a 1 s look at the mailbox and the field also catches a missed pulse;
		 * the blanking's steps in between do not touch the tag */
		bool woke = k_sem_take(&wake, K_MSEC(CLAMP(ms, 0, every))) == 0;
		bool due = k_uptime_get() - look >= every;

		if (!woke && !due) {
			continue;
		}
		if (due) {
			look = k_uptime_get();
		}
		service(due);
	}
}

K_THREAD_DEFINE(caselink_tid, 2048, caselink_thread, NULL, NULL, NULL, 7, 0, K_TICKS_FOREVER);

void caselink_start(void)
{
	k_thread_start(caselink_tid);
}

/* VBUS came or went: look at the mailbox now and pick the matching poll interval */
void caselink_vbus_changed(void)
{
	k_sem_give(&wake);
}

/* DIAG SET 4 (a power measurement): out of the case, keep the GPO input and its pull-up as 0.5.2 did */
void caselink_gpo_pullup_out(bool on)
{
	gpo_pullup_out = on;
	k_sem_give(&wake);
}
#else
void caselink_set_full(bool on)
{
}

bool caselink_heard(uint32_t ms)
{
	return false;
}

size_t caselink_trace(uint8_t *out, size_t max)
{
	return 0;
}

uint32_t caselink_prop(uint32_t id)
{
	return VM_UNKNOWN;
}

void caselink_start(void)
{
}

void caselink_vbus_changed(void)
{
}

void caselink_gpo_pullup_out(bool on)
{
}

void caselink_battery_tick(void)
{
}
#endif
