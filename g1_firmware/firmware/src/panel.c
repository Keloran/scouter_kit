/* JBD013VGA panel on SPIM4 at 32 MHz, driven directly the way stock drives it through nrfx (decomp
 * main/spi_master_init, emulator trace runs/aud-stock): mode 0, MSB first, ORC 0xFF, RXDELAY 2, CS by
 * GPIO active low, SCK/MOSI/CS outputs and MISO input with high drive (H0H1); pins configured only while
 * the panel is powered and released (disconnected inputs) before its rails go off, as stock's deinit.
 * Zephyr's SPI driver is not used: it caps SPIM4 at 16 MHz unless the core runs at 128 MHz, while stock
 * runs the core at 64 MHz and SPIM4 at 32 MHz. Init sequence and the per-frame 97 are stock's
 * (runs/v0-stocktrace-*, g1-fw docs/g1-panel-power-sequence.md). Every panel transaction is one DMA
 * transfer framed by CS (the emulator's panel model frames commands per DMA transfer); the rows of a
 * DRAW are pipelined: the next row is staged while the current one is on the wire. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/irq.h>
#include <zephyr/devicetree.h>
#include <zephyr/drivers/pinctrl.h>
#include <zephyr/sys/crc.h>
#include <zephyr/logging/log.h>
#include <soc.h>
#include <hal/nrf_spim.h>
#include <hal/nrf_gpio.h>
#include "g1.h"

LOG_MODULE_REGISTER(panel, LOG_LEVEL_INF);

#define HDR 5
#define CHUNK_ROWS 8          /* full-width rows per window of a DRAW */
#define POLL_BYTES (HDR + PANEL_ROW_BYTES + 1)
#define POLL_US 400
#define SPI4_NODE DT_NODELABEL(spi4)
#define PINS DT_CHILD(DT_PINCTRL_BY_IDX(SPI4_NODE, 0, 0), group1)
#define PSEL(i) DT_PROP_BY_IDX(PINS, psels, i)
BUILD_ASSERT(NRF_GET_FUN(PSEL(0)) == NRF_FUN_SPIM_SCK && NRF_GET_FUN(PSEL(1)) == NRF_FUN_SPIM_MOSI &&
	     NRF_GET_FUN(PSEL(2)) == NRF_FUN_SPIM_MISO, "spi4 pinctrl group1: SCK, MOSI, MISO");
#define P_SCK NRF_GET_PIN(PSEL(0))
#define P_MOSI NRF_GET_PIN(PSEL(1))
#define P_MISO NRF_GET_PIN(PSEL(2))
#define P_CS NRF_DT_GPIOS_TO_PSEL(SPI4_NODE, cs_gpios)
#define RXDELAY 2

static NRF_SPIM_Type *const spim = NRF_SPIM4;
static K_SEM_DEFINE(spim_end, 0, 1);

/* JBD013 datasheet 7.3.6: Sync (97) reloads the cache into the pixel latch; the cache must not be
 * written during the reload (0.83 ms with the internal 9.6 MHz oscillator, which the stock
 * `31 04` keeps). So nothing is sent to the panel until 1 ms after a 97. */
#define SYNC_GUARD_US 1000

static uint8_t stage[2][HDR + PANEL_ROW_BYTES + 1] __aligned(4);
static uint8_t chunk[2][HDR + CHUNK_ROWS * PANEL_ROW_BYTES + 1] __aligned(4);
static bool on, ok;
static K_MUTEX_DEFINE(panel_lock);   /* every SPIM4 user: power on/off, DRAW, brightness, probe, read */
static bool sync_recent;
static uint32_t clear_ms, power_on_ms;
static uint8_t standby;   /* 2: PANEL = 2, deep power-down with VCOM off; 1 (DIAG SET 6 only): VCOM on */
static bool blanked;      /* the LEDs off around a case poll (panel_blank) */
static uint32_t sync_at_us;

/* Stock's user brightness levels 0..42 (decomp update_brightness_level_for_jdb_panel @0x80ace,
 * set_brightness_to_panel_reg @0x4a830; 42 is stock's maximum, 21 its default user level). The panel shows
 * the lowest of BRIGHTNESS (level), BRIGHTNESS_MAX and the panel guard's BRIGHTNESS_CAP (VM.md §6.6). */
static uint8_t level = PANEL_LEVEL_DEFAULT, level_max = PANEL_LEVEL_MAX, cap = PANEL_LEVEL_MAX;
static uint16_t luminance;
static uint8_t current;

bool panel_is_on(void) { return on; }
bool panel_in_standby(void) { return standby != 0; }
bool panel_ok(void) { return ok; }
uint8_t panel_level(void) { return level; }
uint8_t panel_level_max(void) { return level_max; }
uint8_t panel_cap(void) { return cap; }
uint8_t panel_effective_level(void) { return MIN(level, MIN(level_max, cap)); }

static void spim_isr(const void *arg)
{
	ARG_UNUSED(arg);
	if (nrf_spim_event_check(spim, NRF_SPIM_EVENT_END)) {
		nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
		k_sem_give(&spim_end);
	}
}

/* Stock's spi_master_init pin setup at 32 MHz (nrfx: outputs with the input buffer disconnected, all
 * H0H1; PIN_CNF 0x303 SCK/MOSI/CS, 0x300 MISO), then SPIM4 itself. off: SPIM4 disabled and the pins back
 * to disconnected inputs (0x2), as stock's spi_master_deinit, so nothing drives an unpowered panel. */
static void spim_pins(bool en)
{
	static bool irq_done;

	if (!irq_done) {
		IRQ_CONNECT(SPIM4_IRQn, 1, spim_isr, NULL, 0);
		irq_enable(SPIM4_IRQn);
		irq_done = true;
	}
	if (!en) {
		nrf_spim_int_disable(spim, NRF_SPIM_ALL_INTS_MASK);
		nrf_spim_disable(spim);
		nrf_gpio_cfg_default(P_SCK);
		nrf_gpio_cfg_default(P_MOSI);
		nrf_gpio_cfg_default(P_MISO);
		nrf_gpio_cfg_default(P_CS);
		return;
	}
	nrf_gpio_pin_clear(P_SCK);
	nrf_gpio_pin_clear(P_MOSI);
	nrf_gpio_pin_set(P_CS);
	nrf_gpio_cfg(P_SCK, NRF_GPIO_PIN_DIR_OUTPUT, NRF_GPIO_PIN_INPUT_DISCONNECT, NRF_GPIO_PIN_NOPULL,
		     NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
	nrf_gpio_cfg(P_MOSI, NRF_GPIO_PIN_DIR_OUTPUT, NRF_GPIO_PIN_INPUT_DISCONNECT, NRF_GPIO_PIN_NOPULL,
		     NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
	nrf_gpio_cfg(P_MISO, NRF_GPIO_PIN_DIR_INPUT, NRF_GPIO_PIN_INPUT_CONNECT, NRF_GPIO_PIN_NOPULL,
		     NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
	nrf_gpio_cfg(P_CS, NRF_GPIO_PIN_DIR_OUTPUT, NRF_GPIO_PIN_INPUT_DISCONNECT, NRF_GPIO_PIN_NOPULL,
		     NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
	nrf_spim_pins_set(spim, P_SCK, P_MOSI, P_MISO);
	nrf_spim_frequency_set(spim, NRF_SPIM_FREQ_32M);
	nrf_spim_configure(spim, NRF_SPIM_MODE_0, NRF_SPIM_BIT_ORDER_MSB_FIRST);
	nrf_spim_orc_set(spim, 0xFF);
	nrf_spim_iftiming_set(spim, RXDELAY);
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	k_sem_reset(&spim_end);
	nrf_spim_int_enable(spim, NRF_SPIM_INT_END_MASK);
	nrf_spim_enable(spim);
}

static void guard_sync(void)
{
	if (sync_recent) {
		uint32_t el = now_us() - sync_at_us;

		if (el < SYNC_GUARD_US) {
			k_busy_wait(SYNC_GUARD_US - el);
		}
		sync_recent = false;
	}
}

/* buffers must be in RAM (EasyDMA) */
static void spim_start(const uint8_t *tx, size_t n, uint8_t *rx, size_t rn)
{
	nrf_spim_tx_buffer_set(spim, tx, n);
	nrf_spim_rx_buffer_set(spim, rx, rn);
	nrf_gpio_pin_clear(P_CS);
	nrf_spim_task_trigger(spim, NRF_SPIM_TASK_START);
}

static int spim_wait(void)
{
	if (k_sem_take(&spim_end, K_MSEC(10)) == 0) {
		return 0;
	}
	nrf_spim_task_trigger(spim, NRF_SPIM_TASK_STOP);
	k_busy_wait(20);
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	k_sem_reset(&spim_end);
	LOG_ERR("SPIM4 transfer timeout");
	return -ETIMEDOUT;
}

/* JBD013: single commands keep the last SI value > 1 us after CS goes high (Tcmdh) */
static void cs_off(void)
{
	nrf_gpio_pin_set(P_CS);
	k_busy_wait(1);
}

static int xfer(const uint8_t *tx, size_t n, uint8_t *rx, size_t rn)
{
	static uint8_t tb[8] __aligned(4), rb[8] __aligned(4);
	int err;

	if (n > sizeof(tb) || rn > sizeof(rb)) {
		return -EINVAL;
	}
	guard_sync();
	memcpy(tb, tx, n);
	spim_start(tb, n, rx ? rb : NULL, rx ? rn : 0);
	err = spim_wait();
	cs_off();
	if (rx) {
		memcpy(rx, rb, rn);
	}
	if (n == 1 && tx[0] == 0x97) {
		sync_recent = true;
		sync_at_us = now_us();
	}
	return err;
}

static int cmd(const uint8_t *b, size_t n)
{
	return xfer(b, n, NULL, 0);
}

#define CMD(...) cmd((const uint8_t[]){__VA_ARGS__}, sizeof((const uint8_t[]){__VA_ARGS__}))

/* A pixel transfer of up to a row (~82 us at 32 MHz) ends sooner than an interrupt and a wake-up would
 * take: it runs with the END interrupt off and the event register is polled. */
static void spim_start_polled(const uint8_t *tx, size_t n)
{
	nrf_spim_int_disable(spim, NRF_SPIM_INT_END_MASK);
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	nrf_spim_tx_buffer_set(spim, tx, n);
	nrf_spim_rx_buffer_set(spim, NULL, 0);
	nrf_gpio_pin_clear(P_CS);
	nrf_spim_task_trigger(spim, NRF_SPIM_TASK_START);
}

static int spim_wait_polled(void)
{
	uint32_t t0 = k_cycle_get_32(), limit = k_us_to_cyc_ceil32(POLL_US);
	int err = 0;

	for (uint32_t i = 1; !nrf_spim_event_check(spim, NRF_SPIM_EVENT_END); i++) {
		if (!(i & 255) && k_cycle_get_32() - t0 >= limit) {
			nrf_spim_task_trigger(spim, NRF_SPIM_TASK_STOP);
			k_busy_wait(20);
			LOG_ERR("SPIM4 transfer timeout");
			err = -ETIMEDOUT;
			break;
		}
	}
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	nrf_spim_int_enable(spim, NRF_SPIM_INT_END_MASK);
	return err;
}

/* Pipelined rows: push() starts a transfer once the previous one has ended; the caller alternates
 * between two sets of stage buffers, so it never writes a buffer that is on the wire. `done` counts the
 * rows whose transfers ended. */
struct rowpipe {
	bool busy, short_xfer;
	int rows, err, done;
};

/* pixel windows are cache writes, not single commands: CS goes high without the commands' Tcmdh */
static void pipe_finish(struct rowpipe *p)
{
	if (p->busy) {
		if ((p->short_xfer ? spim_wait_polled() : spim_wait()) == 0) {
			p->done += p->rows;
		} else {
			p->err = -EIO;
		}
		nrf_gpio_pin_set(P_CS);
		p->busy = false;
	}
}

static void pipe_push_ex(struct rowpipe *p, const uint8_t *buf, size_t len, int rows)
{
	if (p->busy) {
		pipe_finish(p);
	} else {
		guard_sync();
	}
	if (!p->err) {
		p->short_xfer = len <= POLL_BYTES;
		if (p->short_xfer) {
			spim_start_polled(buf, len);
		} else {
			spim_start(buf, len, NULL, 0);
		}
		p->busy = true;
		p->rows = rows;
	}
}

static void window_header(uint8_t *h, uint8_t op, int x, int y)
{
	uint32_t c = (x & 0x3FF) | ((y << 10) & 0x7FC00);

	h[0] = op;
	h[1] = c >> 16;
	h[2] = c >> 8;
	h[3] = c;
	h[4] = 0xFF;
}

/* the whole cache to 0 in windows of CHUNK_ROWS rows; the last window's trailing 0 addresses past the panel */
static int clear_all(bool latch)
{
	struct rowpipe p = {0};

	memset(chunk, 0, sizeof(chunk));
	for (int y = 0, k = 0; y < PANEL_H && !p.err; y += CHUNK_ROWS, k ^= 1) {
		window_header(chunk[k], 0x02, 0, y);
		pipe_push_ex(&p, chunk[k], sizeof(chunk[k]), CHUNK_ROWS);
	}
	pipe_finish(&p);
	return p.err | (latch ? CMD(0x97) : 0);
}

/* Stock's level -> (luminance, current) mapping, update_brightness_level_for_jdb_panel @0x80ace, with
 * base = the luminance base from the settings record (board.c, stock's 1..63 rule, default 63):
 *   level < 6:  d = base*2/9; lum = ((level*111)/6 + 1)*(base + 1) if d == 0 or base < 6,
 *               else ((level*(base - d))/6 + d)*111; current 0
 *   level >= 6: lum = (base + 1)*111; current = level - 6
 *   lum 0 -> 1. (Stock's extra scaling by a "lum coefficient" of 70..99 % is only set by a phone
 *   command in a test mode; its default leaves lum unscaled, as here.) */
static void level_regs(uint8_t lv, uint16_t *lum, uint8_t *cur)
{
	uint32_t b = g1_settings.lum_base, d = b * 2 / 9, l;

	if (lv < 6) {
		l = (d == 0 || b < 6) ? ((lv * 111) / 6 + 1) * (b + 1) : ((lv * (b - d)) / 6 + d) * 111;
		*cur = 0;
	} else {
		l = (b + 1) * 111;
		*cur = lv - 6;
	}
	*lum = l ? l : 1;
}

uint16_t panel_luminance(void)
{
	uint16_t l;
	uint8_t c;

	level_regs(panel_effective_level(), &l, &c);
	return l;
}

/* stock's set_brightness_to_panel_reg: 06, A9, 36 lum, 46 cur, 31 04, A3, 97, 1 ms */
static int brightness(void)
{
	int err = 0;

	level_regs(panel_effective_level(), &luminance, &current);
	err |= CMD(0x06);
	err |= CMD(0xA9);
	err |= CMD(0x36, luminance >> 8, luminance & 0xFF);
	err |= CMD(0x46, current);
	err |= CMD(0x31, 0x04);
	if (!blanked) {
		err |= CMD(0xA3);
	}
	err |= CMD(0x97);
	k_msleep(1);
	return err;
}

static int init_sequence(void)
{
	static const uint8_t c0[5][2] = {{0x00, 0x00}, {0x00, 0x14}, {0x18, 0x00}, {0x18, 0x14}, {0x0C, 0x0A}};
	uint8_t rx[4] = {0};
	int err = 0;

	err |= CMD(0x66);
	err |= CMD(0x99);
	k_msleep(10);
	err |= CMD(0x06);
	err |= CMD(0x01, 0x10);
	clear_ms = k_uptime_get_32();
	err |= clear_all(true);
	clear_ms = k_uptime_get_32() - clear_ms;
	for (int i = 0; i < 5; i++) {
		err |= CMD(0xC0, c0[i][0], c0[i][1]);
		err |= CMD(0x97);
		k_msleep(1);
	}
	err |= CMD(0x71);
	err |= CMD(0x97);
	k_msleep(1);
	err |= CMD(0x73);
	err |= CMD(0x97);
	k_msleep(1);
	err |= brightness();
	err |= xfer((const uint8_t[]){0x9F, 0x00, 0x00, 0x00}, 4, rx, 4);
	if (err || ((rx[2] << 8) | rx[3]) != 0x4010) {
		LOG_ERR("panel ID 0x%02x%02x (err %d), expected 0x4010", rx[2], rx[3], err);
		return -ENODEV;
	}
	return 0;
}

static int power_on_locked(void);
static void miso_connect(bool on);

int panel_power_on(void)
{
	int err;

	k_mutex_lock(&panel_lock, K_FOREVER);
	if (on) {
		err = ok ? 0 : -ENODEV;
	} else {
		err = power_on_locked();
	}
	k_mutex_unlock(&panel_lock);
	return err;
}

/* A second try after a failed ID check: on the arm the first power-on during the case's short VBUS
 * pulses read ID 0x0000 twice, and the next one 0.5 s later worked (docs/power.md §8). */
/* out of standby: the panel kept its registers and its cleared cache, so release, the brightness registers
 * (display enable and sync among them) and an ID check are enough */
static int wake_locked(void)
{
	uint8_t rx[4] = {0};
	uint32_t t0 = k_uptime_get_32();
	int err = 0;

	if (standby == 2) {
		board_panel_enable(true);
		k_msleep(1);
	}
	cap = guard_start_cap();
	miso_connect(true);
	nrf_spim_enable(spim);
	standby = 0;
	on = true;
	err |= xfer((const uint8_t[]){0xAB, 0x00, 0x00, 0x00}, 4, rx, 4);
	err |= brightness();
	err |= xfer((const uint8_t[]){0x9F, 0x00, 0x00, 0x00}, 4, rx, 4);
	ok = !err && ((rx[2] << 8) | rx[3]) == 0x4010;
	power_on_ms = k_uptime_get_32() - t0;
	LOG_INF("panel on from standby in %u ms%s", power_on_ms, ok ? "" : ": ID check failed");
	return ok ? 0 : -ENODEV;
}

static int power_on_locked(void)
{
	uint32_t t0 = k_uptime_get_32();

	if (standby && wake_locked() == 0) {
		return 0;
	}
	if (on) {
		CMD(0xB9, 0xFF);
		spim_pins(false);
		pmic_panel_rails(false);
		on = false;
	}

	for (int attempt = 0; attempt < 2; attempt++) {
		if (pmic_panel_rails(true) != 0) {
			ok = false;
			return -ENODEV;
		}
		uint32_t t1 = k_uptime_get_32();

		cap = guard_start_cap();
		spim_pins(true);
		on = true;
		ok = init_sequence() == 0;
		if (ok) {
			power_on_ms = k_uptime_get_32() - t0;
			LOG_INF("panel on in %u ms (rails %u, init %u, clear %u), ID 0x4010, level %u (lum %u, current %u, "
				"base %u), cap %u", power_on_ms, t1 - t0, k_uptime_get_32() - t1, clear_ms, level, luminance,
				current, g1_settings.lum_base, cap);
			return 0;
		}
		panel_power_off();
		k_msleep(100);
	}
	return -ENODEV;
}

void panel_power_off(void)
{
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (on) {
		CMD(0xB9, 0xFF);
	}
	if (on || standby) {
		spim_pins(false);
		pmic_panel_rails(false);
		on = false;
		standby = 0;
	}
	k_mutex_unlock(&panel_lock);
}

/* The LEDs off (A9: no pixel driven, the cache kept) or on again (A3 and a 97; datasheet 7.3.7-7.3.8) while the
 * case powers an arm whose panel is allowed in the case (caselink.c); kept over power-ons (brightness() leaves the
 * display disabled), and a DRAW meanwhile goes to the cache */
int panel_blank(bool b)
{
	int err = 0;

	k_mutex_lock(&panel_lock, K_FOREVER);
	if (on && ok && b && !blanked) {
		err = CMD(0xA9);
	} else if (on && ok && !b && blanked) {
		err = CMD(0xA3);
		err |= CMD(0x97);
	}
	blanked = b;
	k_mutex_unlock(&panel_lock);
	return err;
}

/* MISO floats while CS is high and the panel's rails are on: in standby its input buffer is disconnected */
static void miso_connect(bool on)
{
	nrf_gpio_cfg(P_MISO, NRF_GPIO_PIN_DIR_INPUT, on ? NRF_GPIO_PIN_INPUT_CONNECT : NRF_GPIO_PIN_INPUT_DISCONNECT,
		     NRF_GPIO_PIN_NOPULL, NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
}

/* deep power-down with the rails on (mode 2: VCOM off too), the cache cleared first unless it is still the
 * power-on's, so the next power-on is a short wake (wake_locked); CS stays driven high */
static int sleep_locked(uint8_t mode, bool clear)
{
	int err = clear ? clear_all(true) : 0;

	err |= CMD(0xA9);
	err |= CMD(0xB9, 0xFF);
	nrf_spim_disable(spim);
	miso_connect(false);
	if (mode == 2) {
		board_panel_enable(false);
	}
	on = false;
	standby = mode;
	return err;
}

/* PANEL = 2 (VM.md §2.5): standby, also from off (a full power-on first, so the next DRAW wakes fast) */
int panel_power_standby(void)
{
	int err = 0;
	bool fresh = false;

	k_mutex_lock(&panel_lock, K_FOREVER);
	if (standby == 2) {
		k_mutex_unlock(&panel_lock);
		return 0;
	}
	if (!on || standby) {
		err = power_on_locked();
		fresh = err == 0;
	}
	if (!err && on && ok) {
		err = sleep_locked(2, !fresh);
		LOG_INF("panel standby%s", err ? ": SPI error" : "");
	}
	k_mutex_unlock(&panel_lock);
	return err;
}

/* DIAG SET 6 (test builds, a power experiment): mode 1 or 2 takes a panel that is on to standby (1: VCOM
 * stays on); mode 0 turns a panel in standby fully off */
int panel_standby(uint8_t mode)
{
	int err = 0;

	k_mutex_lock(&panel_lock, K_FOREVER);
	if (mode && on && ok) {
		err = sleep_locked(mode, true);
	} else if (!mode && standby) {
		spim_pins(false);
		pmic_panel_rails(false);
		standby = 0;
	} else if (mode) {
		err = -EINVAL;
	}
	k_mutex_unlock(&panel_lock);
	LOG_WRN("DIAG: panel standby %u (%d)", mode, err);
	return err;
}

uint32_t panel_power_on_ms(void)
{
	return power_on_ms;
}

/* every pixel to 0, not latched: the next DRAW's 97 shows it (the microphone's PANEL = 0, exec.c) */
int panel_clear_all(void)
{
	int err = -ENODEV;

	k_mutex_lock(&panel_lock, K_FOREVER);
	if (on && ok) {
		err = clear_all(false);
	}
	k_mutex_unlock(&panel_lock);
	return err;
}

/* BRIGHTNESS (CTRL 0x0A): level 0..42 as stock, 0xFF = query. Applied at once if the panel is on (then
 * the 37/47 registers are read back), else at the next power-on. out: PANEL_LEVEL_LEN bytes. */
int panel_set_level(uint8_t lv, uint8_t *out)
{
	uint8_t r37[3] = {0}, r47[2] = {0};
	int err = 0;

	if (lv != 0xFF && lv > PANEL_LEVEL_MAX) {
		return -EINVAL;
	}
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (lv != 0xFF) {
		level = lv;
	}
	level_regs(panel_effective_level(), &luminance, &current);
	if (on && ok) {
		if (lv != 0xFF) {
			err = brightness();
		}
		err |= xfer((const uint8_t[]){0x37, 0x00, 0x00}, 3, r37, 3);
		err |= xfer((const uint8_t[]){0x47, 0x00}, 2, r47, 2);
	}
	out[0] = level;
	put16(&out[1], luminance);
	out[3] = current;
	put16(&out[4], (r37[1] << 8) | r37[2]);
	out[6] = r47[1];
	out[7] = g1_settings.lum_base;
	out[8] = (on && ok) ? 1 : 0;
	k_mutex_unlock(&panel_lock);
	if (lv != 0xFF) {
		LOG_INF("brightness level %u: lum %u current %u, read back %u/%u%s", level, luminance, current,
			get16(&out[4]), out[6], (on && ok) ? "" : " (panel off: at next power-on)");
	}
	return err ? -EIO : 0;
}

/* BRIGHTNESS_MAX: applied at once if the panel is on, else at the next power-on */
void panel_set_level_max(uint8_t max)
{
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (max != level_max) {
		uint8_t was = panel_effective_level();

		level_max = max;
		if (on && ok && panel_effective_level() != was) {
			brightness();
		}
	}
	k_mutex_unlock(&panel_lock);
}

/* the panel guard's cap, from the PMIC thread: it waits for the panel lock at most `wait` (a DRAW in
 * progress), else the next second tries again */
int panel_set_cap(uint8_t c, k_timeout_t wait)
{
	if (k_mutex_lock(&panel_lock, wait) != 0) {
		return -EAGAIN;
	}
	if (c != cap) {
		uint8_t was = panel_effective_level();

		cap = c;
		if (on && ok && panel_effective_level() != was) {
			brightness();
		}
	}
	k_mutex_unlock(&panel_lock);
	return 0;
}

/* one row of the cache with 03 (SPI Cache Read: the 02 address format, one dummy byte, then data) into rx;
 * its CRC-32 in *crc */
static int read_row(int y, uint8_t *rx, uint32_t *crc)
{
	static uint8_t tx[HDR] __aligned(4);
	int err;

	window_header(tx, 0x03, 0, y);
	spim_start(tx, HDR, rx, HDR + PANEL_ROW_BYTES);
	err = spim_wait();
	cs_off();
	*crc = crc32_ieee(&rx[HDR], PANEL_ROW_BYTES);
	return err;
}

/* PANEL_READ (CTRL 0x0B, diagnostic): the rows' CRC-32 (zlib's) as read back from the panel's cache, plus the
 * first 16 bytes, so the host can compare them with its mirror of what it drew. Each row is read twice and a
 * third time when the two differ, and its bytes are those two reads agree on: a read can be disturbed (the
 * left arm's first check after an install, while the case pulsed its charging power), the cache is not. Three
 * different reads (the left arm's first check after a cold power-on, 2026-11-04) are followed by two at 8 MHz. */
int panel_read_rows(uint16_t y0, uint16_t rows, uint8_t *out)
{
	uint8_t *a = stage[0], *b = stage[1];
	uint32_t crc = 0, ca, cb;
	int n = 0, err = 0;

	if (y0 >= PANEL_H || rows == 0 || rows > PANEL_H - y0) {
		return -EINVAL;
	}
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (!on || !ok) {
		k_mutex_unlock(&panel_lock);
		return -ENODEV;
	}
	memset(out, 0, PANEL_READ_LEN);
	guard_sync();
	for (int y = y0; y < y0 + rows && !err; y++) {
		err = read_row(y, a, &ca) | read_row(y, b, &cb);
		if (!err && ca != cb) {
			uint32_t first = cb;

			err = read_row(y, b, &cb);
			LOG_WRN("panel read-back: row %d read %08x then %08x, then %08x", y, ca, first, cb);
			if (!err && cb != ca && cb != first) {
				nrf_spim_frequency_set(spim, NRF_SPIM_FREQ_8M);
				err = read_row(y, a, &ca) | read_row(y, b, &cb);
				nrf_spim_frequency_set(spim, NRF_SPIM_FREQ_32M);
				LOG_WRN("panel read-back: row %d at 8 MHz %08x, %08x", y, ca, cb);
				err = err ? err : ca != cb ? -EIO : 0;
			}
			a = cb == ca ? a : b;
		}
		if (!err) {
			crc = crc32_ieee_update(crc, &a[HDR], PANEL_ROW_BYTES);
			if (n++ == 0) {
				memcpy(&out[6], &a[HDR], 16);
			}
		}
		a = stage[0];
	}
	k_mutex_unlock(&panel_lock);
	put32(&out[0], crc);
	put16(&out[4], n);
	return err ? -EIO : 0;
}

/* ---- PANEL_PROBE (diagnostic). Rails on as stock (LDSW1, BUCK2 1.2 V, P0.24 then 15 ms), then stock's
 * ID read `9F 00 00 00` in every SPI mode and MISO sample delay at one clock rate, polling SPIM4 with
 * its interrupt off. Writes to the panel: only `9F 00 00 00`, plus 66/99 or AB if asked. */
static int probe_xfer(const uint8_t *tx, size_t n, uint8_t *rx, bool cs_high, bool cpol)
{
	static uint8_t txb[8] __aligned(4), rxb[8] __aligned(4);
	int i;

	memcpy(txb, tx, n);
	memset(rxb, 0, sizeof(rxb));
	nrf_gpio_pin_write(P_SCK, cpol);
	nrf_spim_tx_buffer_set(spim, txb, n);
	nrf_spim_rx_buffer_set(spim, rxb, n);
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	nrf_gpio_pin_write(P_CS, cs_high);
	k_busy_wait(1);
	nrf_spim_task_trigger(spim, NRF_SPIM_TASK_START);
	for (i = 0; i < 2000 && !nrf_spim_event_check(spim, NRF_SPIM_EVENT_END); i++) {
		k_busy_wait(1);
	}
	k_busy_wait(1);
	nrf_gpio_pin_write(P_CS, !cs_high);
	k_busy_wait(5);   /* Tcmdh > 1 us after CSB high */
	if (rx) {
		memcpy(rx, rxb, n);
	}
	return i < 2000 ? 0 : -ETIMEDOUT;
}

/* MISO level with the pin's pull-up (bit 0) and pull-down (bit 1): a driven line reads the same both
 * ways, a floating one follows the pull. */
static uint8_t miso_pulls(void)
{
	uint8_t bits = 0;

	nrf_gpio_cfg_input(P_MISO, NRF_GPIO_PIN_PULLUP);
	k_busy_wait(50);
	bits |= nrf_gpio_pin_read(P_MISO) ? 1 : 0;
	nrf_gpio_cfg_input(P_MISO, NRF_GPIO_PIN_PULLDOWN);
	k_busy_wait(50);
	bits |= nrf_gpio_pin_read(P_MISO) ? 2 : 0;
	nrf_gpio_cfg(P_MISO, NRF_GPIO_PIN_DIR_INPUT, NRF_GPIO_PIN_INPUT_CONNECT, NRF_GPIO_PIN_NOPULL,
		     NRF_GPIO_PIN_H0H1, NRF_GPIO_PIN_NOSENSE);
	return bits;
}

int panel_probe(uint8_t mhz, uint8_t flags, uint16_t delay_ms, uint8_t *out)
{
	static const uint8_t rdid[4] = {0x9F, 0x00, 0x00, 0x00};
	bool cs_high = flags & PROBE_CS_HIGH;
	nrf_spim_frequency_t f;
	int err = 0, k = 0;

	switch (mhz) {
	case 32: f = NRF_SPIM_FREQ_32M; break;
	case 16: f = NRF_SPIM_FREQ_16M; break;
	case 8: f = NRF_SPIM_FREQ_8M; break;
	case 4: f = NRF_SPIM_FREQ_4M; break;
	case 2: f = NRF_SPIM_FREQ_2M; break;
	case 1: f = NRF_SPIM_FREQ_1M; break;
	default: return -EINVAL;
	}
	if (delay_ms > 1000) {
		return -EINVAL;
	}
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (on) {
		k_mutex_unlock(&panel_lock);
		return -EBUSY;
	}
	memset(out, 0, PANEL_PROBE_LEN);
	out[0] = mhz;
	out[1] = flags;
	out[2] = delay_ms & 0xFF;
	out[3] = delay_ms >> 8;

	if (flags & PROBE_LDSW2) {
		err |= pmic_ldsw2_on();
	}
	if (flags & PROBE_EN_FIRST) {
		board_panel_enable(true);
		k_msleep(4);
	}
	err |= pmic_panel_rails(true);
	spim_pins(true);
	nrf_spim_int_disable(spim, NRF_SPIM_ALL_INTS_MASK);
	nrf_gpio_pin_write(P_CS, !cs_high);
	k_msleep(delay_ms);

	out[4] = miso_pulls();
	nrf_gpio_pin_write(P_CS, cs_high);
	k_busy_wait(5);
	out[4] |= miso_pulls() << 2;
	nrf_gpio_pin_write(P_CS, !cs_high);
	k_busy_wait(5);

	nrf_spim_frequency_set(spim, f);
	if (flags & PROBE_RESET) {
		err |= probe_xfer((const uint8_t[]){0x66}, 1, NULL, cs_high, false);
		k_msleep(1);
		err |= probe_xfer((const uint8_t[]){0x99}, 1, NULL, cs_high, false);
		k_msleep(10);
	}
	if (flags & PROBE_AB) {
		err |= probe_xfer((const uint8_t[]){0xAB, 0x00, 0x00, 0x00}, 4, &out[6], cs_high, false);
		k_msleep(1);
	}
	for (int mode = 0; mode < 4; mode++) {
		for (int rxd = 0; rxd < 8; rxd++, k++) {
			nrf_spim_configure(spim, (nrf_spim_mode_t)mode, NRF_SPIM_BIT_ORDER_MSB_FIRST);
			nrf_spim_iftiming_set(spim, rxd);
			err |= probe_xfer(rdid, 4, &out[10 + 4 * k], cs_high, mode >= 2);
		}
	}

	nrf_gpio_pin_write(P_CS, 1);
	nrf_gpio_pin_write(P_SCK, 0);
	nrf_spim_event_clear(spim, NRF_SPIM_EVENT_END);
	spim_pins(false);
	NVIC_ClearPendingIRQ(SPIM4_IRQn);
	err |= pmic_panel_rails(false);
	k_mutex_unlock(&panel_lock);
	out[5] = err ? 1 : 0;
	LOG_INF("panel probe %u MHz flags 0x%02x delay %u ms: miso %x, mode0/rxd2 %02x%02x%02x%02x%s", mhz, flags,
		delay_ms, out[4], out[10 + 8], out[11 + 8], out[12 + 8], out[13 + 8], err ? " (errors)" : "");
	return err ? -EIO : 0;
}

/* the C library's memcpy is a byte loop */
static void copy_px(uint8_t *d, const uint8_t *s, int n)
{
	int i = 0;

	for (; i + 4 <= n; i += 4) {
		uint32_t v;

		__builtin_memcpy(&v, s + i, 4);
		__builtin_memcpy(d + i, &v, 4);
	}
	for (; i < n; i++) {
		d[i] = s[i];
	}
}

/* A DRAW as a stream of rows (exec.c: the VM core packs each row, panel_rect_row sends it): pixel windows
 * (0x02) into the panel's cache, then one 0x97 in panel_rect_end, which latches the whole cache into the
 * visible pixel array (datasheet 4.1.4/4.2.1), so no partial DRAW is ever seen. Writes exactly the w x h
 * pixels of the clipped rectangle (for VIDEO: the parts of rows that panel_rect_span gets).
 * The 02 address is a pixel address, but the panel stores a pixel only once 4 more clocks follow it
 * ("final 4 dummy clocks", datasheet 7.3.15) and SPI moves whole bytes, so a window of n data bytes
 * stores 2n - 1 pixels; the address wraps from column 639 to the next row. Full-width rows go out as
 * windows of up to CHUNK_ROWS rows, each but the last with a trailing 0 byte whose extra pixel lands on the
 * next window's first pixel, which overwrites it before the 97. Other rows are one window each: an odd
 * width (w + 1) / 2 bytes, an even width w / 2 bytes (w - 1 pixels) plus a one-byte window for its last
 * pixel, sent before the next row; the last full-width pixel of a DRAW the same way. A window is staged
 * while the previous one is on the wire. */
static uint8_t tail[2][HDR + 1] __aligned(4);
static struct {
	struct rowpipe p;
	int x, y, w, h, r, k, fill;
	bool full, tail_pending;
} rect;

int panel_rect_begin(int x, int y, int w, int h)
{
	k_mutex_lock(&panel_lock, K_FOREVER);
	if (!on || !ok) {
		k_mutex_unlock(&panel_lock);
		return -ENODEV;
	}
	memset(&rect, 0, sizeof(rect));
	rect.x = x;
	rect.y = y;
	rect.w = w;
	rect.h = h;
	rect.full = x == 0 && w == PANEL_W;
	return 0;
}

static void full_row(const uint8_t *px)
{
	uint8_t *c = chunk[rect.k];

	if (!rect.fill) {
		window_header(c, 0x02, 0, rect.y + rect.r);
	}
	copy_px(c + HDR + rect.fill * PANEL_ROW_BYTES, px, PANEL_ROW_BYTES);
	rect.fill++;
	rect.r++;
	if (rect.fill < CHUNK_ROWS && rect.r < rect.h) {
		return;
	}
	size_t n = HDR + rect.fill * PANEL_ROW_BYTES;

	if (rect.r < rect.h) {
		c[n++] = 0;
		pipe_push_ex(&rect.p, c, n, rect.fill);
	} else {
		pipe_push_ex(&rect.p, c, n, rect.fill - 1);
		window_header(tail[rect.k], 0x02, PANEL_W - 1, rect.y + rect.r - 1);
		tail[rect.k][HDR] = c[n - 1] << 4;
		pipe_push_ex(&rect.p, tail[rect.k], HDR + 1, 1);
	}
	rect.fill = 0;
	rect.k ^= 1;
}

/* one row of w pixels at (x, y) as its own window */
static void window_row(int x, int y, int w, const uint8_t *px)
{
	uint8_t *s = stage[rect.k];
	int n = (w + 1) / 2;
	bool split = !(w & 1);

	window_header(s, 0x02, x, y);
	copy_px(s + HDR, px, n);
	if (rect.tail_pending) {
		pipe_push_ex(&rect.p, tail[rect.k ^ 1], HDR + 1, 1);
		rect.tail_pending = false;
	}
	pipe_push_ex(&rect.p, s, HDR + n, split ? 0 : 1);
	if (split) {
		window_header(tail[rect.k], 0x02, x + w - 1, y);
		tail[rect.k][HDR] = s[HDR + w / 2 - 1] << 4;
		rect.tail_pending = true;
	}
	rect.k ^= 1;
}

void panel_rect_row(const uint8_t *px)
{
	if (rect.full) {
		full_row(px);
		return;
	}
	window_row(rect.x, rect.y + rect.r, rect.w, px);
	rect.r++;
}

/* VIDEO: part of a row inside the rectangle; rows top to bottom, not every one */
void panel_rect_span(int x, int y, int w, const uint8_t *px)
{
	window_row(x, y, w, px);
}

int panel_rect_end(void)
{
	if (rect.tail_pending) {
		pipe_push_ex(&rect.p, tail[rect.k ^ 1], HDR + 1, 1);
	}
	pipe_finish(&rect.p);
	CMD(0x97);
	k_mutex_unlock(&panel_lock);
	return rect.p.err ? rect.p.err : rect.p.done;
}
