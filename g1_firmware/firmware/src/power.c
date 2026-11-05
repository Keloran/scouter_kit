/* Power telemetry (CTRL POWER 0x0E, PROTOCOL.md §8.6): the nPM1300 samples of pmic.c, converted as the
 * stock firmware's NCS v2.5.1 npm1300_charger driver does (IBAT full scale = the discharge limit or the
 * charge current, NTC beta 4250 as stock's fuel gauge), plus per-state accumulators so a host can read
 * the average current, temperatures and CPU load of states it is not connected in (advertising,
 * charging) after the fact. Read only: nothing here changes the arm's behaviour. */
#include <math.h>
#include <string.h>
#include <zephyr/kernel.h>
#include "g1.h"

#define DISCHARGE_FS_UA 1000000   /* stock: discharge limit 1.0 A (ISETDISCHARGE 0x9A,0x01) */
#define CHARGE_FS_UA 38000        /* stock: charge current 38 mA (ISETCHARGE 0x09,0x01) */
#define NTC_BETA 4250.0f          /* stock's fuel gauge: 65.01 degC for the emulator's B4250 NTC at 65 */
#define RING 64
#define STATES 8                  /* bit 0 panel on, bit 1 host connected, bit 2 VBUS */

struct acc {
	uint32_t samples, ua_samples;
	int64_t sum_ua;
	int32_t sum_ntc_dc, sum_die_dc;
	uint64_t busy_cycles, all_cycles;
};

static struct pmic_sample last;
static int32_t ring[RING];
static uint32_t samples, ua_samples, invalid;
static int64_t sum_ua;
static int32_t ibat_ua, avg_ua;
static bool ibat_ok;
static int16_t ntc_dc = INT16_MIN, die_dc = INT16_MIN;
static uint16_t cpu_permille;
static struct acc accs[STATES];
static struct acc win;
static uint16_t win_vbat_first, win_vbat_last;
static bool win_on;
static uint64_t prev_all, prev_busy;
static struct k_spinlock lock;
volatile uint8_t power_imu_regs[3] = {0xFF, 0xFF, 0xFF};
volatile bool power_imu_configured;

static int32_t to_ua(uint8_t stat, uint16_t code, bool *valid)
{
	*valid = true;
	switch (stat) {
	case 0x04:
		return (int32_t)((int64_t)code * DISCHARGE_FS_UA / 1024);
	case 0x0C:
		return -(int32_t)((int64_t)code * (CHARGE_FS_UA / 10) / 1024);
	case 0x0D:
		return -(int32_t)((int64_t)code * (CHARGE_FS_UA / 2) / 1024);
	case 0x0F:
		return -(int32_t)((int64_t)code * CHARGE_FS_UA / 1024);
	default:
		*valid = false;
		return 0;
	}
}

static int16_t to_ntc_dc(uint16_t code)
{
	if (code == 0 || code >= 1024) {
		return INT16_MIN;
	}
	float inv_t = 1.0f / 298.15f - logf(1024.0f / code - 1.0f) / NTC_BETA;
	return (int16_t)lroundf((1.0f / inv_t - 273.15f) * 10.0f);
}

/* nPM1300 die temperature: 394.67 - 0.7926 x code degC */
static int16_t to_die_dc(uint16_t code)
{
	return (int16_t)((3946700 - 7926 * (int32_t)code) / 1000);
}

static uint8_t state_index(void)
{
	return (panel_is_on() ? 1 : 0) | (proto_connected() ? 2 : 0) | ((last.vbus_status & 1) ? 4 : 0);
}

void power_sample(const struct pmic_sample *s)
{
	k_thread_runtime_stats_t st;
	uint64_t all = 0, busy = 0;
	bool valid;
	int32_t ua = to_ua(s->ibat_stat & 0x0F, s->ibat_code, &valid);
	struct acc *a;
	k_spinlock_key_t key;

	if (k_thread_runtime_stats_all_get(&st) == 0) {
		all = st.execution_cycles;
		busy = st.total_cycles;
	}
	key = k_spin_lock(&lock);
	last = *s;
	ntc_dc = to_ntc_dc(s->ntc_code);
	die_dc = to_die_dc(s->die_code);
	samples++;
	a = &accs[state_index()];
	a->samples++;
	/* ADCIBATMEASSTATUS bit 4 (seen in the case while VBUS comes and goes): an invalid measurement, left
	 * out; other modes than discharge/charge (e.g. 0x08 with the charge complete) are 0 mA as in the
	 * NCS driver */
	ibat_ok = !(s->ibat_stat & 0x10) && valid;
	if (s->ibat_stat & 0x10) {
		invalid++;
	} else {
		ibat_ua = valid ? ua : 0;
		ring[ua_samples % RING] = ibat_ua;
		ua_samples++;
		sum_ua += ibat_ua;
		int64_t r = 0;
		uint32_t n = MIN(ua_samples, RING);
		for (uint32_t i = 0; i < n; i++) {
			r += ring[i];
		}
		avg_ua = (int32_t)(r / n);
		a->ua_samples++;
		a->sum_ua += ibat_ua;
	}
	a->sum_ntc_dc += ntc_dc == INT16_MIN ? 0 : ntc_dc;
	a->sum_die_dc += die_dc;
	if (win_on) {
		uint16_t mv = s->vbat_code * 5000 / 1024;

		win.samples++;
		if (!(s->vbus_status & 1) && !(s->ibat_stat & 0x10)) {
			win.ua_samples++;
			win.sum_ua += ibat_ua;
			win.sum_ntc_dc += ntc_dc == INT16_MIN ? 0 : ntc_dc;
			win_vbat_first = win_vbat_first ? win_vbat_first : mv;
			win_vbat_last = mv;
		}
	}
	if (prev_all && all > prev_all) {
		a->all_cycles += all - prev_all;
		a->busy_cycles += busy - prev_busy;
		if (win_on) {
			win.all_cycles += all - prev_all;
			win.busy_cycles += busy - prev_busy;
		}
		cpu_permille = (uint16_t)((busy - prev_busy) * 1000 / (all - prev_all));
	}
	prev_all = all;
	prev_busy = busy;
	k_spin_unlock(&lock, key);
}

/* a measurement window (the net-off test): IBAT, NTC and VBAT of the samples on battery */
void power_window_start(void)
{
	k_spinlock_key_t key = k_spin_lock(&lock);

	memset(&win, 0, sizeof(win));
	win_vbat_first = win_vbat_last = 0;
	win_on = true;
	k_spin_unlock(&lock, key);
}

void power_window_get(struct netoff_phase *p)
{
	k_spinlock_key_t key = k_spin_lock(&lock);

	p->samples = win.samples;
	p->battery_samples = win.ua_samples;
	p->avg_ua = win.ua_samples ? (int32_t)(win.sum_ua / win.ua_samples) : 0;
	p->ntc_dc = win.ua_samples ? (int16_t)(win.sum_ntc_dc / (int32_t)win.ua_samples) : INT16_MIN;
	p->vbat_first = win_vbat_first;
	p->vbat_last = win_vbat_last;
	p->cpu_permille = win.all_cycles ? (uint16_t)(win.busy_cycles * 1000 / win.all_cycles) : 0;
	win_on = false;
	k_spin_unlock(&lock, key);
}

int32_t power_batt_ua(void) { return avg_ua; }
int32_t power_batt_ma(void) { return ua_samples ? (avg_ua + (avg_ua < 0 ? -500 : 500)) / 1000 : INT32_MIN; }
int32_t power_ibat_ua(void) { return ibat_ua; }

/* one IBAT result (ADCIBATMEASSTATUS, the 10-bit code) in µA, as the samples above; false if not a measurement */
bool power_ibat_code_ua(uint8_t stat, uint16_t code, int32_t *ua)
{
	bool valid;

	*ua = to_ua(stat & 0x0F, code, &valid);
	return valid && !(stat & 0x10);
}
bool power_ibat_ok(void) { return ibat_ok; }
int16_t power_batt_temp(void) { return ntc_dc; }
uint16_t power_vbus_mv(void) { return last.vbus_code * 7500 / 1024; }

static void put64(uint8_t *p, uint64_t v)
{
	put32(p, (uint32_t)v);
	put32(p + 4, (uint32_t)(v >> 32));
}

static size_t reply_body(uint8_t page, uint8_t *b, size_t n);

/* page 0: live values (48 bytes); pages 1..8: the accumulators of state page - 1 (40 bytes). */
size_t power_reply(uint8_t page, uint8_t *b, size_t n)
{
	k_spinlock_key_t key = k_spin_lock(&lock);

	n = reply_body(page, b, n);
	k_spin_unlock(&lock, key);
	return n;
}

static size_t reply_body(uint8_t page, uint8_t *b, size_t n)
{
	if (page >= 1 && page <= STATES) {
		const struct acc *a = &accs[page - 1];

		put32(&b[n], a->samples);
		put32(&b[n + 4], a->ua_samples);
		put64(&b[n + 8], (uint64_t)a->sum_ua);
		put32(&b[n + 16], (uint32_t)a->sum_ntc_dc);
		put32(&b[n + 20], (uint32_t)a->sum_die_dc);
		put64(&b[n + 24], a->busy_cycles);
		put64(&b[n + 32], a->all_cycles);
		return n + 40;
	}
	memset(&b[n], 0, 48);
	put16(&b[n], last.vbat_code * 5000 / 1024);
	put16(&b[n + 2], (uint16_t)(int16_t)(avg_ua / 1000));
	put16(&b[n + 4], (uint16_t)ntc_dc);
	put16(&b[n + 6], (uint16_t)die_dc);
	put32(&b[n + 8], (uint32_t)avg_ua);
	put32(&b[n + 12], (uint32_t)ibat_ua);
	put16(&b[n + 16], power_vbus_mv());
	put16(&b[n + 18], last.vsys_code * 6375 / 1024);
	put16(&b[n + 20], caselink.vrect_mv);
	b[n + 22] = last.charge_status;
	b[n + 23] = last.charge_error;
	b[n + 24] = last.vbus_status;
	b[n + 25] = last.ldsw_status;
	b[n + 26] = last.ibat_stat;
	b[n + 27] = caselink.requests ? caselink.status : 0xFF;
	b[n + 28] = caselink.requests ? caselink.level : 0xFF;
	b[n + 29] = caselink.lid;
	put16(&b[n + 30], (uint16_t)MIN(caselink.requests, 0xFFFF));
	put16(&b[n + 32], caselink.requests ? (uint16_t)MIN((k_uptime_get_32() - caselink.last_ms) / 1000, 0xFFFE)
					    : 0xFFFF);
	put16(&b[n + 34], cpu_permille);
	put32(&b[n + 36], samples);
	b[n + 40] = (IS_ENABLED(CONFIG_G1_NET_ESB_QUIET) ? 1 : 0) | (panel_is_on() ? 2 : 0) |
		    (proto_connected() ? 4 : 0) | (adv_slow() ? 8 : 0) | (IS_ENABLED(CONFIG_UART_CONSOLE) ? 16 : 0) |
		    (caselink.full_latched ? 32 : 0) | (power_imu_configured ? 64 : 0) |
		    (IS_ENABLED(CONFIG_G1_CASE_LINK) ? 128 : 0);
	b[n + 41] = state_index();
	b[n + 42] = power_imu_regs[0];
	b[n + 43] = power_imu_regs[1];
	b[n + 44] = power_imu_regs[2];
	put16(&b[n + 45], (uint16_t)MIN(invalid, 0xFFFF));
	b[n + 47] = (uint8_t)caselink.gpo_events;   /* low byte, wraps: rates from differences */
	return n + 48;
}
