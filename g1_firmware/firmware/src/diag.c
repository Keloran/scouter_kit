/* DIAG sub-ops 3-8 (CTRL 0x09; layouts in g1_firmware/README.md): the board audit and the power work.
 * 3 PIN_PROBE, 4 REGS and 8 IMU only read (PIN_PROBE pulls up unused input pins for a moment); 5 SET
 * and the net-off measurement (6, 7) exist in test builds only. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include <nrf.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(diag, LOG_LEVEL_INF);

enum { D_PIN_PROBE = 3, D_REGS = 4, D_SET = 5, D_NET_OFF = 6, D_NET_OFF_RESULT = 7, D_IMU = 8, D_MIC = 9, D_ENCODE_LOAD = 10,
       D_TOUCH = 11, D_LIGHT = 12, D_PEER = 13, D_CASE = 14, D_CASE_TRACE = 15, D_ADC_BURST = 16, D_GUARD = 17,
       D_ASSETS = 18, D_BONDS = 19 };
#define ADC_RECORD_MAX 64
#define ADC_PER_REPLY 36
enum { SET_TOUCH_RAIL = 1, SET_LDSW2 = 2, SET_TOUCH_MODE = 3, SET_GPO_PULLUP = 4, SET_MIC_128MHZ = 5, SET_PANEL_STANDBY = 6,
       SET_IDLE = 7, SET_CASE_FULL = 8, SET_ASSET_RESET = 9, SET_ASSET_STEP = 10 };
#define NETOFF_MAX_S 2400

/* pins g1v0 and stock leave unconfigured (P1.10, the tag's GPO, keeps its own configuration; P0.02/03 are
 * the SoC's NFC antenna pins) */
#define PROBE_P0 (BIT(6) | BIT(12) | BIT(25) | BIT(27) | BIT(28) | BIT(29) | BIT(31))
#define PROBE_P1 (BIT(4) | BIT(5) | BIT(6) | BIT(7) | BIT(8) | BIT(9) | BIT(10) | BIT(11) | BIT(12) | BIT(15))
#define CNF_PULLUP 0x0000000Cu

/* Only pins that are inputs now (a pin something drives cannot carry an INT line) and that belong to
 * the app core are probed; the reply's masks name them. P1.10 (the tag's GPO) keeps its own pull-up. */
static size_t pin_probe(const uint32_t *want, uint8_t *b, size_t n, uint8_t *st)
{
	static const uint8_t steps[] = {0, 1, 0, 2, 0};
	NRF_GPIO_Type *port[2] = {NRF_P0_S, NRF_P1_S};
	uint32_t mask[2] = {0, 0}, saved[2][32];

	if (!imu_present()) {
		*st = E_ARG;
		return n;
	}
	for (int p = 0; p < 2; p++) {
		for (int i = 0; i < 32; i++) {
			saved[p][i] = port[p]->PIN_CNF[i];
			if ((want[p] & BIT(i)) && !(saved[p][i] & GPIO_PIN_CNF_DIR_Msk) &&
			    !(saved[p][i] & GPIO_PIN_CNF_MCUSEL_Msk)) {
				mask[p] |= BIT(i);
			}
		}
	}
	for (int p = 0; p < 2; p++) {
		for (int i = 0; i < 32; i++) {
			if ((mask[p] & BIT(i)) && !(p == 1 && i == 10)) {
				port[p]->PIN_CNF[i] = CNF_PULLUP;
			}
		}
	}
	put32(&b[n], mask[0]);
	put32(&b[n + 4], mask[1]);
	n += 8;
	for (int s = 0; s < ARRAY_SIZE(steps); s++) {
		if (imu_int_drive(steps[s]) != 0) {
			*st = E_ARG;
		}
		k_busy_wait(200);
		put32(&b[n], port[0]->IN);
		put32(&b[n + 4], port[1]->IN);
		n += 8;
	}
	for (int p = 0; p < 2; p++) {
		for (int i = 0; i < 32; i++) {
			if ((mask[p] & BIT(i)) && !(p == 1 && i == 10)) {
				port[p]->PIN_CNF[i] = saved[p][i];
			}
		}
	}
	if (imu_int_drive(0xFF) != 0) {
		*st = E_ARG;
	}
	return n;
}

/* nPM1300 registers of page 5: rails, load switches, GPIOs, LED drivers, watchdog and ship */
static const uint8_t pmic_regs[][2] = {
	{0x04, 0x08}, {0x04, 0x0A}, {0x04, 0x0C}, {0x04, 0x0E}, {0x04, 0x0F}, {0x04, 0x15}, {0x04, 0x34},
	{0x08, 0x04}, {0x08, 0x05}, {0x08, 0x06}, {0x08, 0x07}, {0x08, 0x08}, {0x08, 0x09}, {0x08, 0x0C},
	{0x08, 0x0D}, {0x06, 0x00}, {0x06, 0x01}, {0x06, 0x02}, {0x06, 0x03}, {0x06, 0x04}, {0x06, 0x0A},
	{0x06, 0x0B}, {0x06, 0x0C}, {0x06, 0x0D}, {0x06, 0x0E}, {0x06, 0x0F}, {0x06, 0x10}, {0x06, 0x11},
	{0x06, 0x12}, {0x06, 0x13}, {0x06, 0x1E}, {0x0A, 0x00}, {0x0A, 0x01}, {0x0A, 0x02}, {0x07, 0x00},
	{0x07, 0x05}, {0x09, 0x00}, {0x0B, 0x04}, {0x03, 0x04}, {0x03, 0x34}, {0x02, 0x07},
};

static size_t regs(uint8_t page, uint8_t *b, size_t n, uint8_t *st)
{
	NRF_GPIO_Type *port[2] = {NRF_P0_S, NRF_P1_S};
	const uint32_t periph[] = {
		NRF_UARTE0_S->ENABLE, NRF_TWIM1_S->ENABLE, NRF_TWIM2_S->ENABLE, NRF_SPIM4_S->ENABLE,
		NRF_SAADC_S->ENABLE, NRF_PDM0_S->ENABLE, NRF_QSPI_S->ENABLE, NRF_CLOCK_S->HFCLKSTAT,
		NRF_CLOCK_S->HFCLK192MSTAT, NRF_CLOCK_S->LFCLKSTAT, NRF_CLOCK_S->HFCLKCTRL,
		NRF_REGULATORS_S->VREGMAIN.DCDCEN, NRF_REGULATORS_S->VREGRADIO.DCDCEN,
		NRF_REGULATORS_S->VREGH.DCDCEN, NRF_RESET_S->NETWORK.FORCEOFF, NRF_WDT0_S->RUNSTATUS,
		NRF_GPIOTE0_S->INTENSET, NRF_P0_S->LATCH, NRF_P1_S->LATCH, NRF_P0_S->DETECTMODE,
	};

	b[n++] = page;
	if (page <= 2) {
		for (int i = 0; i < 16; i++) {
			put32(&b[n + 4 * i], port[page / 2]->PIN_CNF[(page & 1) * 16 + i]);
		}
		return n + 64;
	}
	if (page == 3) {
		for (int p = 0; p < 2; p++) {
			put32(&b[n], port[p]->OUT);
			put32(&b[n + 4], port[p]->IN);
			put32(&b[n + 8], port[p]->DIR);
			n += 12;
		}
		for (int i = 0; i < 8; i++) {
			put32(&b[n + 4 * i], NRF_GPIOTE0_S->CONFIG[i]);
		}
		return n + 32;
	}
	if (page == 4) {
		for (int i = 0; i < ARRAY_SIZE(periph); i++) {
			put32(&b[n + 4 * i], periph[i]);
		}
		return n + 4 * ARRAY_SIZE(periph);
	}
	if (page == 5) {
		for (int i = 0; i < ARRAY_SIZE(pmic_regs); i++) {
			b[n + i] = 0xFF;
			if (pmic_i2c_read(pmic_regs[i][0], pmic_regs[i][1], &b[n + i], 1) != 0) {
				*st = E_ARG;
			}
		}
		return n + ARRAY_SIZE(pmic_regs);
	}
	*st = E_ARG;
	return n;
}

/* the TouchBar controller (AW9320x, touch.c) */
#define TOUCH_CHIPID 0xFF00
#define TOUCH_CMD 0x4408
#define TOUCH_IRQEN 0x4414
#define TOUCH_WST 0x1BAC

static size_t set(uint8_t item, uint8_t value, uint8_t *b, size_t n, uint8_t *st)
{
	uint32_t id, before, after;
	int err = 0;

	switch (item) {
	case SET_TOUCH_RAIL:
		board_touch_power(value != 0);
		LOG_WRN("DIAG: touch rail %s", value ? "on" : "off");
		break;
	case SET_LDSW2:
		if (value) {
			err = pmic_ldsw2_on();
			k_msleep(20);
			imu_init();
		} else {
			imu_off();
			err = pmic_ldsw2_off();
		}
		LOG_WRN("DIAG: LDSW2 %s (%d)", value ? "on" : "off", err);
		break;
	case SET_GPO_PULLUP:
		caselink_gpo_pullup_out(value != 0);
		LOG_WRN("DIAG: GPO pull-up out of the case %s", value ? "on (as 0.5.2)" : "off");
		break;
	case SET_MIC_128MHZ:
		mic_set_fast(value != 0);
		LOG_WRN("DIAG: capture at %s MHz", value ? "128" : "64");
		break;
	case SET_IDLE:   /* the idle bisect's switches (idle.c): exactly those of the mask on */
		b[n++] = idle_set(value);
		break;
	case SET_ASSET_RESET:   /* the next put resets at checkpoint 1-4 (assets.c), as a power failure would; 0 off */
	case SET_ASSET_STEP:    /* 1 pause after every flash call that changes flash, 2 one step, 3 abort the job, 0 off */
		if (value > 4 || (item == SET_ASSET_STEP && value > 3)) {
			err = -EINVAL;
			break;
		}
		assets_set(item, value);
		LOG_WRN("DIAG: asset store %s %u", item == SET_ASSET_RESET ? "reset at checkpoint" : "step", value);
		break;
	case SET_CASE_FULL:   /* a top-up experiment (power.md §15): 0 ends the "full" latch now, as a low VBAT would */
		caselink_set_full(value != 0);
		LOG_WRN("DIAG: case full latch %s", value ? "set" : "ended");
		break;
	case SET_PANEL_STANDBY:   /* 0xFF: only the last power-on's duration */
		err = value == 0xFF ? 0 : panel_standby(value);
		put32(&b[n], panel_power_on_ms());
		n += 4;
		break;
	case SET_TOUCH_MODE:
		err = touch_reg_read(TOUCH_CHIPID, &id);
		touch_reg_read(TOUCH_WST, &before);
		if (!err && value >= 1 && value <= 3) {
			err = touch_reg_write(TOUCH_CMD, value);
			k_msleep(60);
		}
		touch_reg_read(TOUCH_WST, &after);
		put32(&b[n], id);
		put32(&b[n + 4], before);
		put32(&b[n + 8], after);
		n += 12;
		LOG_WRN("DIAG: touch id 0x%08x, mode %u, WST 0x%08x -> 0x%08x (%d)", id, value, before, after, err);
		break;
	default:
		*st = E_ARG;
		return n;
	}
	b[n++] = board_touch_powered();
	b[n++] = imu_present();
	if (err) {
		*st = E_ARG;
	}
	return n;
}

/* body: u8 sub-op, then its arguments; the reply body is appended at b[n] */
size_t diag_request(const uint8_t *a, size_t alen, uint8_t *b, size_t n, uint8_t *st, bool *reboot)
{
	struct netoff_phase p[NETOFF_PHASES];
	uint32_t samples, misses, errors;
	uint16_t seconds;
	uint8_t done, fails;
	int16_t map[9];
	int32_t base;
	bool on;

	switch (alen ? a[0] : 0) {
	case D_PIN_PROBE: {   /* optional u32 P0 mask, u32 P1 mask: probe only these of the candidates */
		uint32_t want[2] = {PROBE_P0, PROBE_P1};

		if (alen >= 9) {
			want[0] &= get32(&a[1]);
			want[1] &= get32(&a[5]);
		}
		return pin_probe(want, b, n, st);
	}
	case D_REGS:
		return regs(alen > 1 ? a[1] : 0, b, n, st);
	case D_SET:
		if (!IS_ENABLED(CONFIG_G1_FAULT_INJECTION) || alen < 3) {
			*st = E_ARG;
			return n;
		}
		return set(a[1], a[2], b, n, st);
	case D_NET_OFF:   /* u16 s, u8 phases[, u8 switch mask per phase (idle.c)] */
		seconds = alen >= 4 ? get16(&a[1]) : 0;
		if (!IS_ENABLED(CONFIG_G1_FAULT_INJECTION) || seconds < 20 || alen < 4 || a[3] < 1 ||
		    a[3] > NETOFF_PHASES || seconds * a[3] > NETOFF_MAX_S || safe_level() ||
		    (alen > 4 && alen != 4u + a[3]) || (alen <= 4 && a[3] > 4)) {
			*st = E_ARG;
			return n;
		}
		retained_netoff_request(seconds, a[3], alen > 4 ? &a[4] : NULL);
		*reboot = true;
		return n;
	case D_IMU:
		imu_stats(&samples, &misses, &errors, &on);
		put32(&b[n], samples);
		put32(&b[n + 4], misses);
		put32(&b[n + 8], errors);
		b[n + 12] = on;
		b[n + 13] = imu_present();
		put32(&b[n + 14], imu_irqs());
		imu_map(map, &base);
		put32(&b[n + 18], (uint32_t)base);
		for (int i = 0; i < 9; i++) {
			put16(&b[n + 22 + 2 * i], (uint16_t)map[i]);
		}
		return n + 40;
	case D_TOUCH: {   /* read only: the driver's state and counters, the controller's ID, mode and event enable */
		uint32_t ev, errs, loads, irqs, v;

		touch_stats(&ev, &errs, &loads, &irqs, &on);
		b[n] = touch_present();
		b[n + 1] = on;
		put32(&b[n + 2], ev);
		put32(&b[n + 6], errs);
		put32(&b[n + 10], loads);
		touch_reg_read(TOUCH_CHIPID, &v);
		put32(&b[n + 14], v);
		touch_reg_read(TOUCH_WST, &v);
		put32(&b[n + 18], v);
		touch_reg_read(TOUCH_IRQEN, &v);
		put32(&b[n + 22], v);
		put32(&b[n + 26], touch_prop(VM_P_TOUCH_GESTURE));
		put32(&b[n + 30], touch_prop(VM_P_WORN));
		put32(&b[n + 34], irqs);
		return n + 38;
	}
	case D_LIGHT: {   /* read only: the light sensor's state and counters, LIGHT, AUTO_BRIGHTNESS */
		uint32_t readings, errs;

		light_stats(&readings, &errs, &on);
		b[n] = light_present();
		b[n + 1] = on;
		b[n + 2] = light_auto();
		put32(&b[n + 3], readings);
		put32(&b[n + 7], errs);
		put32(&b[n + 11], light_prop());
		return n + 15;
	}
	case D_CASE: {   /* read only: IN_CASE and its inputs, the case link's counters and last bytes */
		bool moved, after;
		uint32_t since = g1_case_since_ms(&moved);
		int raw = touch_wear_raw(&after);

		b[n] = g1_in_case();
		b[n + 1] = pmic_vbus_present();
		b[n + 2] = caselink.field;
		b[n + 3] = moved;
		put32(&b[n + 4], since);
		put32(&b[n + 8], caselink.messages);
		put32(&b[n + 12], caselink.requests);
		put32(&b[n + 16], caselink.replies);
		put16(&b[n + 20], caselink.busy);
		put16(&b[n + 22], caselink.unknown);
		put16(&b[n + 24], caselink.long_trip);
		put16(&b[n + 26], (uint16_t)MIN(caselink.restarts, 0xFFFF));
		b[n + 28] = caselink.status;
		b[n + 29] = caselink.box_battery;
		b[n + 30] = caselink.lid;
		b[n + 31] = caselink.gpo_config;
		put32(&b[n + 32], caselink.field_ms ? k_uptime_get_32() - caselink.field_ms : UINT32_MAX);
		b[n + 36] = (uint8_t)(int8_t)raw;
		b[n + 37] = after;
		put32(&b[n + 38], caselink.charger_enables);
		put32(&b[n + 42], caselink.gpo_events);
		put32(&b[n + 46], caselink.i2c_errors);
		put32(&b[n + 50], caselink.by_look);
		put32(&b[n + 54], caselink.reply_n);
		put32(&b[n + 58], caselink.reply_late);
		put32(&b[n + 62], caselink.reply_max_us);
		put32(&b[n + 66], caselink.reply_n ? (uint32_t)(caselink.reply_sum_us / caselink.reply_n) : 0);
		for (int i = 0; i < 8; i++) {
			put32(&b[n + 70 + 4 * i], caselink.it_count[i]);
		}
		put32(&b[n + 102], 0);   /* 102–107: the case hold, CASE_PANEL and its drops until 0.5.58 */
		put16(&b[n + 106], 0);
		put16(&b[n + 108], caselink.vrect_mv);
		put32(&b[n + 110], caselink.blanks);
		put32(&b[n + 114], caselink.blank_pulses);
		put32(&b[n + 118], (uint32_t)(caselink.blanked_ms / 1000));
		b[n + 122] = caselink.blanked;
		return n + 123;
	}
	case D_ADC_BURST: {   /* u8 n, u16 wait µs, u8 first (0: take a new record): VBAT+IBAT conversions with their
			       * times (the panel's pulse, docs/review-dimming.md point 12); per one: u16 t (10 µs since the
			       * first), i16 mA (0x7FFF invalid), u16 mV; up to 36 per reply from `first` */
		static struct adc_conv rec[ADC_RECORD_MAX];
		static uint8_t taken;
		uint8_t want = alen >= 2 ? MIN(a[1], ADC_RECORD_MAX) : ADC_RECORD_MAX, first = alen >= 5 ? a[4] : 0;

		if (first == 0) {
			taken = 0;
			if (pmic_adc_record(want, alen >= 4 ? get16(&a[2]) : 1000, rec) == 0) {
				taken = want;
			}
		}
		b[n++] = taken;
		b[n++] = first;
		for (int i = first; i < taken && i < first + ADC_PER_REPLY; i++) {
			int32_t ma = rec[i].ua == INT32_MIN ? 0x7FFF : rec[i].ua / 1000;

			put16(&b[n], (uint16_t)((rec[i].t_us - rec[0].t_us) / 10));
			put16(&b[n + 2], (uint16_t)(int16_t)ma);
			put16(&b[n + 4], rec[i].mv);
			n += 6;
		}
		return n;
	}
	case D_ASSETS:   /* read only: the asset store's last job step times, counters, single-step state */
		return n + assets_diag(&b[n]);
	case D_BONDS:   /* read only: the bonds, their aging counters and key flags (bonds.c) */
		return n + bonds_diag(&b[n], MAX_PKT - n);
	case D_GUARD:   /* read only: the panel guard's last burst, I_avg, cap and levels (guard.c) */
		guard_state(&b[n]);
		return n + GUARD_STATE_LEN;
	case D_CASE_TRACE:   /* read only: the case link's last events (u32 ms, u8 kind, u8 value) */
		b[n] = caselink_trace(&b[n + 1], MAX_PKT - n - 1) / 6;
		return n + 1 + 6 * b[n];
	case D_PEER: {   /* read only: the arm-to-arm link's state, the net core's last link report, counters */
		struct peer_stats p;

		peer_get_stats(&p);
		b[n] = p.available;
		b[n + 1] = p.link;
		b[n + 2] = p.follow;
		b[n + 3] = p.link_value;
		put32(&b[n + 4], p.reports);
		put32(&b[n + 8], p.net_acked);
		put32(&b[n + 12], p.net_failed);
		put32(&b[n + 16], p.net_received);
		put32(&b[n + 20], p.report_t);
		put32(&b[n + 24], p.frames_out);
		put32(&b[n + 28], p.reports_sent);
		put32(&b[n + 32], p.report_timeouts);
		put32(&b[n + 36], p.send_errors);
		put32(&b[n + 40], p.frames_in);
		put32(&b[n + 44], p.delivered);
		put32(&b[n + 48], p.inbox_drops);
		put32(&b[n + 52], p.levels_applied);
		put32(&b[n + 56], p.last_arrival_t);
		put32(&b[n + 60], p.other_msgs);
		put32(&b[n + 64], (uint32_t)p.offset);
		put32(&b[n + 68], p.delay_last_us);
		put32(&b[n + 72], p.delay_max_us);
		put32(&b[n + 76], p.delay_total_us);
		return n + 80;
	}
	case D_MIC: {   /* counters and levels only: no audio */
		struct mic_stats m;

		mic_get_stats(&m);
		b[n] = m.on;
		b[n + 1] = m.rate;
		b[n + 2] = m.per_event;
		b[n + 3] = mic_available();
		put32(&b[n + 4], m.starts);
		put32(&b[n + 8], m.frames);
		put32(&b[n + 12], m.lost);
		put32(&b[n + 16], m.encoded);
		put32(&b[n + 20], m.events);
		put32(&b[n + 24], proto_mic_drops());
		put32(&b[n + 28], m.faults);
		put32(&b[n + 32], m.encode_us);
		put16(&b[n + 36], m.encode_max_us);
		put16(&b[n + 38], m.peak);
		put16(&b[n + 40], m.rms);
		return n + 42;
	}
	case D_ENCODE_LOAD:   /* u16 seconds, u8 128 MHz: the encoder alone on a synthetic tone, no audio */
		if (!IS_ENABLED(CONFIG_G1_FAULT_INJECTION) || alen < 4 || mic_encode_load(get16(&a[1]), a[3]) != 0) {
			*st = E_ARG;
		}
		return n;
	case D_NET_OFF_RESULT:
		done = retained_netoff_result(p, &seconds, &fails);
		b[n] = done;
		put16(&b[n + 1], seconds);
		b[n + 3] = fails;
		b[n + 4] = retained_net_fails();
		n += 5;
		for (int i = 0; i < done; i++) {
			put32(&b[n], p[i].samples);
			put32(&b[n + 4], p[i].battery_samples);
			put32(&b[n + 8], (uint32_t)p[i].avg_ua);
			put16(&b[n + 12], p[i].vbat_first);
			put16(&b[n + 14], p[i].vbat_last);
			put16(&b[n + 16], (uint16_t)p[i].ntc_dc);
			put16(&b[n + 18], p[i].cpu_permille);
			n += 20;
		}
		return n;
	}
	*st = E_ARG;
	return n;
}

/* Phase i of the net-off measurement: with masks the idle switches of mask i (idle.c), and all off at the end
 * (i < 0); else 0 network core off; 1 also the touch rail off; 2 also LDSW2 (IMU, microphone) off; 3 network
 * core off with everything else on again (repeatability). */
static void phase(int i, const uint8_t *masks)
{
	if (masks) {
		idle_set(i < 0 ? 0 : masks[i]);
		return;
	}
	if (i < 0) {
		i = 3;
	}
	board_touch_power(i != 1 && i != 2);
	if (i == 2) {
		imu_off();
		pmic_ldsw2_off();
	} else if (!imu_present()) {
		pmic_ldsw2_on();
		k_msleep(20);
		imu_init();
	}
}

/* main(), at boot: a pending net-off request holds the network core off (no Bluetooth, no ESB) and
 * measures the phases with the PMIC's samples, then stores the result and reboots normally. RAM only:
 * the request is taken (cleared) before anything else, so any reset returns to a normal boot; both
 * watchdogs stay armed; the health check reboots at the deadline; nothing is written to flash. */
bool diag_netoff_boot(void)
{
	struct netoff_phase res[NETOFF_PHASES] = {0};
	uint8_t masks[NETOFF_PHASES];
	uint16_t seconds;
	uint8_t phases;
	bool masked;

	if (!IS_ENABLED(CONFIG_G1_FAULT_INJECTION) || !retained_netoff_take(&seconds, &phases, masks, &masked)) {
		return false;
	}
	NRF_RESET_S->NETWORK.FORCEOFF = RESET_NETWORK_FORCEOFF_FORCEOFF_Hold;
	health_no_bt_checks(k_uptime_get_32() + (20 + phases * (10 + seconds) + 120) * 1000U);
	LOG_WRN("net-off measurement: network core held off, %u phases of %u s", phases, seconds);
	pmic_wait_ready(K_SECONDS(10));
	k_sleep(K_SECONDS(10));
	for (int i = 0; i < phases; i++) {
		phase(i, masked ? masks : NULL);
		k_sleep(K_SECONDS(5));
		power_window_start();
		k_sleep(K_SECONDS(seconds));
		power_window_get(&res[i]);
		LOG_INF("net-off phase %d: %u samples, %u on battery, IBAT %d uA, VBAT %u -> %u mV, NTC %d, cpu %u/1000",
			i, res[i].samples, res[i].battery_samples, res[i].avg_ua, res[i].vbat_first, res[i].vbat_last,
			res[i].ntc_dc, res[i].cpu_permille);
	}
	phase(-1, masked ? masks : NULL);
	retained_netoff_store(res, phases);
	k_sleep(K_SECONDS(1));
	g1_reboot(CLEAN_HOST, false, false);
	return true;
}
