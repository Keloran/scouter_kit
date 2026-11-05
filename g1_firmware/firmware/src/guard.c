/* The panel guard (VM.md §6.6, docs/review-dimming.md in the development tree): while the panel is on and the arm runs
 * on its battery, BRIGHTNESS_CAP keeps the mean discharge current under CONFIG_G1_PANEL_BUDGET_MA and VBAT away from
 * the nPM1300's 2.8 V POF. The PMIC thread runs it once per second on that round's burst (pmic.c: the mean IBAT of 16
 * conversions over 40 ms or more, and their lowest VBAT) and NTC. The budget rules use I_avg, an exponential average
 * of the rounds' means (alpha 1/8); an attack goes in one step to the level that the full-white table predicts for
 * the content shown. From level 6 up only the panel's current register (level - 6) changes; level 6 is the floor. */
#include <zephyr/kernel.h>
#include <zephyr/logging/log.h>
#include "g1.h"

LOG_MODULE_REGISTER(guard, LOG_LEVEL_INF);

#define FLOOR 6
#define BUDGET_UA (CONFIG_G1_PANEL_BUDGET_MA * 1000)

static atomic_t enabled = ATOMIC_INIT(1);
static uint16_t v_rest;                        /* the last VBAT sample with the panel off or in standby */
static int32_t avg_ua = -1;                    /* I_avg; -1 until the first round since the panel came on */
static uint32_t attack_ms;                     /* the last attack on the budget, at most one per 4 s */
static bool attacked;
static uint8_t calm_s;                         /* seconds in a row that allow a release */
static bool sparse;                            /* and all of them had I_avg <= 50 % of the budget */
static uint8_t noticed = PANEL_LEVEL_MAX;      /* the cap of the last NOTICE 7 */
static int32_t last_ua;
static uint16_t last_mv;
static uint8_t last_n;
static uint32_t attacks;

/* full white on the whole panel, both arms (docs/power.md §17): mA at these levels, linear between */
static const uint8_t white_lv[] = {0, 6, 12, 21, 30, 42};
static const uint16_t white_ma[] = {0, 15, 80, 190, 275, 355};

static uint32_t white_ua(uint8_t level)
{
	for (int i = 1; i < ARRAY_SIZE(white_lv); i++) {
		if (level <= white_lv[i]) {
			uint32_t l0 = white_lv[i - 1], l1 = white_lv[i];

			return (white_ma[i - 1] * (l1 - level) + white_ma[i] * (level - l0)) * 1000 / (l1 - l0);
		}
	}
	return white_ma[ARRAY_SIZE(white_ma) - 1] * 1000;
}

/* the largest level whose full-white current, times the content factor ua / I_white(level shown), stays within
 * 95 % of the budget; floor 6 */
static uint8_t table_level(uint8_t eff, int32_t ua)
{
	uint64_t room = (uint64_t)BUDGET_UA * 95 / 100 * white_ua(eff);
	uint8_t l = PANEL_LEVEL_MAX;

	while (l > FLOOR && (uint64_t)ua * white_ua(l) > room) {
		l--;
	}
	return l;
}

/* the cap at a panel power-on (from off or standby), from the rest voltage */
uint8_t guard_start_cap(void)
{
	uint16_t v = v_rest;

	if (!atomic_get(&enabled) || pmic_vbus_present() || v == 0) {
		return PANEL_LEVEL_MAX;
	}
	return v >= 3600 ? PANEL_LEVEL_MAX : 24;
}

/* PANEL_GUARD (CTRL 0x16): off, the cap is 42 and no NOTICE 7 is sent */
void guard_set(bool on)
{
	if (atomic_set(&enabled, on) == on) {
		return;
	}
	LOG_INF("panel guard %s", on ? "on" : "off");
	if (!on) {
		noticed = PANEL_LEVEL_MAX;
		panel_set_cap(PANEL_LEVEL_MAX, K_NO_WAIT);
	}
}

bool guard_on(void)
{
	return atomic_get(&enabled);
}

int32_t guard_avg_ua(void)
{
	return avg_ua < 0 ? -1000 : avg_ua;
}

/* DIAG GUARD: the last burst's mean (mA) and lowest VBAT, its valid conversions, I_avg (mA, -1 none), the cap, the
 * level shown and written, on, the release count, the attacks, the budget (mA) */
void guard_state(uint8_t *out)
{
	put16(&out[0], (uint16_t)(int16_t)(last_ua / 1000));
	put16(&out[2], (uint16_t)(int16_t)(avg_ua < 0 ? -1 : avg_ua / 1000));
	put16(&out[4], last_mv);
	out[6] = last_n;
	out[7] = panel_cap();
	out[8] = panel_effective_level();
	out[9] = panel_level();
	out[10] = atomic_get(&enabled);
	out[11] = calm_s;
	put32(&out[12], attacks);
	put16(&out[16], CONFIG_G1_PANEL_BUDGET_MA);
}

static void attack_log(uint16_t mv, int32_t ua, int16_t dc, uint8_t to)
{
	if (attacks++ < 8 || attacks % 100 == 0) {
		LOG_INF("panel guard: %u mV, %d mA average, %d.%d C: cap %u -> %u (written %u, %u attacks)", mv, ua / 1000,
			dc / 10, (dc < 0 ? -dc : dc) % 10, panel_cap(), to, panel_level(), attacks);
	}
}

void guard_tick(bool fresh)
{
	uint8_t cap = panel_cap(), eff = panel_effective_level(), next = cap, n;
	uint32_t now = k_uptime_get_32();
	int16_t dc = power_batt_temp();
	bool dc_ok = dc != INT16_MIN;
	int32_t ua;
	uint16_t mv;

	if (!atomic_get(&enabled) || pmic_vbus_present() || !panel_is_on()) {
		if (!panel_is_on() && fresh && pmic_status.vbat_mv) {
			v_rest = pmic_status.vbat_mv;
		}
		next = PANEL_LEVEL_MAX;   /* also while the panel is off or in standby, so BRIGHTNESS_CAP reads 42 */
		avg_ua = -1;
		attacked = false;
		calm_s = 0;
	} else if (fresh && pmic_burst(&ua, &mv, &n)) {
		bool hot = dc_ok && dc >= 380, budget = false;
		uint8_t to = cap;

		last_ua = ua;
		last_mv = mv;
		last_n = n;
		avg_ua = avg_ua < 0 ? ua : avg_ua + (ua - avg_ua) / 8;
		if ((avg_ua > BUDGET_UA || hot) && (!attacked || now - attack_ms >= 4000)) {
			uint8_t l = table_level(eff, MAX(avg_ua, hot ? BUDGET_UA + 1 : 0));

			if (l < eff) {
				to = l;
				budget = true;
			}
		}
		if (mv < 3150) {
			to = MIN(to, eff > FLOOR + 2 ? eff - 2 : FLOOR);
		}
		if (mv < 3000) {
			to = MIN(to, FLOOR + (eff > FLOOR ? eff - FLOOR : 0) / 2);
		}
		to = MAX(to, FLOOR);
		if (to < cap && to < eff) {
			attack_log(mv, avg_ua, dc, to);
			if (budget) {
				attack_ms = now;
				attacked = true;
			}
			/* what the new level should draw for the same content, so the average does not lag into a second step */
			avg_ua = (int32_t)((int64_t)avg_ua * white_ua(to) / MAX(white_ua(eff), 1));
			next = to;
			calm_s = 0;
		} else if (avg_ua <= BUDGET_UA * 85 / 100 && mv >= 3400 && (!dc_ok || dc < 360)) {
			if (calm_s++ == 0) {
				sparse = true;
			}
			sparse = sparse && avg_ua <= BUDGET_UA / 2;
			if (calm_s >= 5) {
				next = MIN(cap + (sparse ? 3 : 1), PANEL_LEVEL_MAX);
				calm_s = 0;
			}
		} else {
			calm_s = 0;
		}
	}
	if (next != cap) {
		panel_set_cap(next, K_MSEC(200));
	}
	cap = panel_cap();
	if (cap != noticed && atomic_get(&enabled)) {
		noticed = cap;
		proto_notice(N_GUARD, (const uint8_t[]){cap, panel_effective_level()}, 2);
	}
}
