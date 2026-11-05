/* Microphone capture (VM.md §6.5, review-v5.md §4): PDM0 on the right arm as stock (CLK P1.13, DIN P1.14,
 * 1.28 MHz with ratio 80 = 16 kHz mono, sampled on the falling edge, default gain, HFXO), 10 ms DMA
 * buffers, each encoded by liblc3 into one frame (40 B at 32 kbps, 20 B at 16 kbps); frames_per_event
 * consecutive frames make one MIC_DATA event. The first 40 ms after a start are dropped (the microphone
 * starts at full scale) and a 13 Hz DC blocker precedes the encoder. Only the executor starts and stops it,
 * for the MIC control request; audio never reaches the arena. A stop disables PDM0 at once and leaves P1.13
 * low. */
#include <math.h>
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/irq.h>
#include <zephyr/drivers/clock_control.h>
#include <zephyr/drivers/clock_control/nrf_clock_control.h>
#include <zephyr/logging/log.h>
#include <hal/nrf_pdm.h>
#include <hal/nrf_gpio.h>
#include <hal/nrf_clock.h>
#include <soc.h>
#include "lc3.h"
#include "g1.h"

LOG_MODULE_REGISTER(mic, LOG_LEVEL_INF);

#define PDM NRF_PDM0_S
#define PIN_CLK NRF_GPIO_PIN_MAP(1, 13)
#define PIN_DIN NRF_GPIO_PIN_MAP(1, 14)
#define SAMPLES 160
#define NBUF 6
#define SPARE NBUF
#define LEVEL_FRAMES 100
#define SETTLE_FRAMES 4
#define DC_POLE 0.995f

struct frame_ref {
	uint8_t buf, gen;
	uint32_t seq;
};

static int16_t pcm[NBUF + 1][SAMPLES] __aligned(4);
static atomic_t busy;
static int8_t filling, latched;
static uint8_t next_buf, gen;
static uint32_t seq;
static float dc_x1, dc_y1;
static volatile bool capturing, running, broken;
static bool fast = IS_ENABLED(CONFIG_G1_MIC_128MHZ);
static volatile int64_t load_until, lease_until;
static int64_t last_frame_ms;
static uint8_t frame_bytes, per_event;
static lc3_encoder_mem_16k_t enc_mem;
static lc3_encoder_t enc;
static struct onoff_manager *hfxo;
static struct onoff_client hfxo_cli;
static bool hfxo_on;
static struct mic_stats stats;
static uint64_t level_sum;
static uint16_t level_frames;
K_MSGQ_DEFINE(pcm_q, sizeof(struct frame_ref), NBUF, 4);
static K_MUTEX_DEFINE(lock);

static int take_free(void)
{
	for (int i = 0; i < NBUF; i++) {
		int k = (next_buf + i) % NBUF;

		if (!atomic_test_and_set_bit(&busy, k)) {
			next_buf = k + 1;
			return k;
		}
	}
	return SPARE;
}

/* a buffer is full: to the encoder, or lost (the counter still advances, so the host sees the gap) */
static void complete(int k)
{
	struct frame_ref f = {.buf = k, .gen = gen, .seq = seq++};

	stats.frames++;
	if (k == SPARE || k_msgq_put(&pcm_q, &f, K_NO_WAIT) != 0) {
		if (k != SPARE) {
			atomic_clear_bit(&busy, k);
		}
		stats.lost++;
	}
}

/* STARTED: the buffer latched last is now being filled, so the one before it is complete; latch the next */
static void pdm_isr(const void *arg)
{
	if (nrf_pdm_event_check(PDM, NRF_PDM_EVENT_STARTED)) {
		nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_STARTED);
		if (capturing) {
			if (filling >= 0) {
				complete(filling);
			}
			filling = latched;
			latched = take_free();
			nrf_pdm_buffer_set(PDM, (uint32_t *)pcm[latched], SAMPLES);
		}
	}
	if (nrf_pdm_event_check(PDM, NRF_PDM_EVENT_STOPPED)) {
		nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_STOPPED);
	}
	if (nrf_pdm_event_check(PDM, NRF_PDM_EVENT_END)) {
		nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_END);
	}
}

static void level(const int16_t *x)
{
	for (int i = 0; i < SAMPLES; i++) {
		int32_t v = x[i];

		level_sum += (uint64_t)(v * v);
		stats.peak = MAX(stats.peak, (uint16_t)(v < 0 ? -v : v));
	}
	if (++level_frames == LEVEL_FRAMES) {
		stats.rms = (uint16_t)sqrtf((float)(level_sum / (LEVEL_FRAMES * SAMPLES)));
		level_sum = 0;
		level_frames = 0;
	}
}

static void dc_block(int16_t *x)
{
	for (int i = 0; i < SAMPLES; i++) {
		float v = x[i], y = v - dc_x1 + DC_POLE * dc_y1;

		dc_x1 = v;
		dc_y1 = y;
		x[i] = (int16_t)CLAMP(lroundf(y), INT16_MIN, INT16_MAX);
	}
}

static void core_clock(bool f)
{
	nrf_clock_hfclk_div_set(NRF_CLOCK_S, f ? NRF_CLOCK_HFCLK_DIV_1 : NRF_CLOCK_HFCLK_DIV_2);
	SystemCoreClockUpdate();
}

/* DIAG ENCODE_LOAD (test builds): the encoder's work alone, on a synthetic tone, 100 frames a second for
 * `seconds`, with no PDM, no microphone and nothing sent: its energy at 64 or 128 MHz, measured without audio */
static void encode_load(void)
{
	static int16_t x[SAMPLES];
	static uint8_t out[40];
	int64_t next = k_uptime_get();

	for (int i = 0; i < SAMPLES; i++) {
		x[i] = (int16_t)(3000.0f * sinf(6.2831853f * 440.0f * i / 16000.0f));
	}
	k_mutex_lock(&lock, K_FOREVER);
	enc = lc3_setup_encoder(10000, 16000, 0, &enc_mem);
	core_clock(fast);
	while (k_uptime_get() < load_until) {
		uint32_t t0 = k_cycle_get_32();

		lc3_encode(enc, LC3_PCM_FORMAT_S16, x, 1, 40, out);
		stats.encode_us += k_cyc_to_us_floor32(k_cycle_get_32() - t0);
		stats.encoded++;
		next += 10;
		k_mutex_unlock(&lock);
		k_sleep(K_TIMEOUT_ABS_MS(next));
		k_mutex_lock(&lock, K_FOREVER);
	}
	core_clock(false);
	load_until = 0;
	k_mutex_unlock(&lock);
}

int mic_encode_load(uint16_t seconds, uint8_t clock_fast)
{
	if (running || load_until || !enc || seconds > 900) {
		return -EBUSY;
	}
	memset(&stats, 0, sizeof(stats));
	fast = clock_fast;
	load_until = k_uptime_get() + seconds * 1000LL;
	k_msgq_put(&pcm_q, &(struct frame_ref){.buf = SPARE, .gen = 0xFF}, K_NO_WAIT);
	return 0;
}

void mic_set_fast(bool f)
{
	fast = f;
}

/* VM.md §6.5: a MIC 1 with the format in force renews the lease */
void mic_renew(void)
{
	if (running) {
		lease_until = k_uptime_get() + MIC_LEASE_MS;
	}
}

/* a capture fault: the executor stops the microphone (NOTICE 5, reason 2) and HELLO.board bit 2 clears */
static void fault(void)
{
	stats.faults++;
	broken = true;
	exec_mic_fault();
}

/* one frame per 10 ms buffer; the lock is held per frame, so a stop never lets a later event out */
static void mic_thread(void *a, void *b, void *c)
{
	static uint8_t frames[MIC_MAX_FRAMES_BYTES];
	struct frame_ref f;
	uint8_t cur = 0xFF, n = 0;
	uint16_t first = 0;

	for (;;) {
		if (k_msgq_get(&pcm_q, &f, running ? K_MSEC(100) : K_FOREVER) != 0) {
			k_mutex_lock(&lock, K_FOREVER);
			if (running && k_uptime_get() - last_frame_ms > 250 && !broken) {
				LOG_ERR("no PDM samples for 250 ms");
				fault();
			}
			k_mutex_unlock(&lock);
			continue;
		}
		if (running && k_uptime_get() > lease_until) {
			LOG_WRN("microphone lease not renewed: off");
			lease_until = INT64_MAX;
			exec_mic_lease_end();
		}
		if (f.buf == SPARE && f.gen == 0xFF && load_until) {
			encode_load();
			continue;
		}
		k_mutex_lock(&lock, K_FOREVER);
		if (f.buf == SPARE) {
			k_mutex_unlock(&lock);
			continue;
		}
		last_frame_ms = k_uptime_get();
		if (!running || f.gen != gen) {
			atomic_clear_bit(&busy, f.buf);
			k_mutex_unlock(&lock);
			continue;
		}
		if (f.seq < SETTLE_FRAMES) {
			atomic_clear_bit(&busy, f.buf);
			k_mutex_unlock(&lock);
			continue;
		}
		uint16_t c = f.seq - SETTLE_FRAMES;

		if (f.gen != cur || (n && (uint16_t)(first + n) != c)) {
			if (f.gen == cur && n) {
				proto_mic_data(first, n, frames, n * frame_bytes);
				stats.events++;
			}
			if (f.gen != cur) {
				dc_x1 = pcm[f.buf][0];
				dc_y1 = 0.0f;
			}
			cur = f.gen;
			n = 0;
		}
		if (!n) {
			first = c;
		}
		dc_block(pcm[f.buf]);
		uint32_t t0 = k_cycle_get_32();
		int err = lc3_encode(enc, LC3_PCM_FORMAT_S16, pcm[f.buf], 1, frame_bytes, &frames[n * frame_bytes]);
		uint32_t us = k_cyc_to_us_floor32(k_cycle_get_32() - t0);

		level(pcm[f.buf]);
		atomic_clear_bit(&busy, f.buf);
		stats.encode_us += us;
		stats.encode_max_us = MAX(stats.encode_max_us, us);
		stats.encoded++;
		if (err) {
			n = 0;
			fault();
		} else if (++n == per_event) {
			proto_mic_data(first, n, frames, n * frame_bytes);
			stats.events++;
			n = 0;
		}
		k_mutex_unlock(&lock);
	}
}

K_THREAD_DEFINE(mic, 4096, mic_thread, NULL, NULL, NULL, 5, 0, 0);

bool mic_available(void)
{
	return board_role() == 0 && enc != NULL && !broken && !load_until;
}

static bool hfxo_request(void)
{
	int res = -EAGAIN;

	if (!hfxo) {
		hfxo = z_nrf_clock_control_get_onoff(CLOCK_CONTROL_NRF_SUBSYS_HF);
	}
	sys_notify_init_spinwait(&hfxo_cli.notify);
	if (onoff_request(hfxo, &hfxo_cli) < 0) {
		return false;
	}
	hfxo_on = true;
	for (int i = 0; i < 50 && sys_notify_fetch_result(&hfxo_cli.notify, &res) == -EAGAIN; i++) {
		k_usleep(100);
	}
	return res == 0;
}

/* executor thread only */
int mic_start(uint8_t rate, uint8_t fpe)
{
	if (!mic_available() || running || load_until) {
		return -EINVAL;
	}
	frame_bytes = rate ? 20 : 40;
	per_event = CLAMP(fpe, 1, MIN(MIC_MAX_PER_EVENT, MIC_MAX_FRAMES_BYTES / frame_bytes));
	if (!hfxo_request()) {
		LOG_ERR("HFXO did not start");
		broken = true;
		mic_stop();
		return -EIO;
	}
	core_clock(fast);
	k_mutex_lock(&lock, K_FOREVER);
	enc = lc3_setup_encoder(10000, 16000, 0, &enc_mem);
	gen++;
	seq = 0;
	filling = -1;
	next_buf = 0;
	atomic_clear(&busy);
	k_msgq_purge(&pcm_q);
	uint32_t starts = stats.starts + 1;

	memset(&stats, 0, sizeof(stats));
	stats.on = 1;
	stats.rate = rate;
	stats.per_event = per_event;
	stats.starts = starts;
	level_sum = 0;
	level_frames = 0;
	nrf_gpio_pin_clear(PIN_CLK);
	nrf_gpio_cfg_output(PIN_CLK);
	nrf_gpio_cfg_input(PIN_DIN, NRF_GPIO_PIN_NOPULL);
	nrf_pdm_psel_connect(PDM, PIN_CLK, PIN_DIN);
	nrf_pdm_clock_set(PDM, NRF_PDM_FREQ_1280K);
	nrf_pdm_ratio_set(PDM, NRF_PDM_RATIO_80X);
	nrf_pdm_mclksrc_configure(PDM, NRF_PDM_MCLKSRC_PCLK32M);
	nrf_pdm_mode_set(PDM, NRF_PDM_MODE_MONO, NRF_PDM_EDGE_LEFTFALLING);
	nrf_pdm_gain_set(PDM, NRF_PDM_GAIN_DEFAULT, NRF_PDM_GAIN_DEFAULT);
	nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_STARTED);
	nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_STOPPED);
	nrf_pdm_event_clear(PDM, NRF_PDM_EVENT_END);
	latched = take_free();
	nrf_pdm_buffer_set(PDM, (uint32_t *)pcm[latched], SAMPLES);
	nrf_pdm_int_enable(PDM, NRF_PDM_INT_STARTED);
	running = true;
	capturing = true;
	last_frame_ms = k_uptime_get();
	lease_until = last_frame_ms + MIC_LEASE_MS;
	irq_enable(PDM0_IRQn);
	nrf_pdm_enable(PDM);
	nrf_pdm_task_trigger(PDM, NRF_PDM_TASK_START);
	k_mutex_unlock(&lock);
	/* the thread blocks without a timeout while off: wake it into its no-sample watch */
	k_msgq_put(&pcm_q, &(struct frame_ref){.buf = SPARE}, K_NO_WAIT);
	LOG_INF("microphone on: %u B frames, %u per event", frame_bytes, per_event);
	return 0;
}

/* executor thread only; immediate: the clock stops with the peripheral, P1.13 stays low, and under the
 * lock no frame being encoded can make an event after it */
void mic_stop(void)
{
	bool was = running;

	capturing = false;
	irq_disable(PDM0_IRQn);
	nrf_pdm_int_disable(PDM, NRF_PDM_INT_STARTED);
	nrf_pdm_task_trigger(PDM, NRF_PDM_TASK_STOP);
	nrf_pdm_disable(PDM);
	nrf_pdm_psel_disconnect(PDM);
	nrf_gpio_pin_clear(PIN_CLK);
	nrf_gpio_cfg_output(PIN_CLK);
	nrf_gpio_cfg_default(PIN_DIN);
	k_mutex_lock(&lock, K_FOREVER);
	running = false;
	gen++;
	stats.on = 0;
	proto_mic_flush();
	if (!load_until) {
		core_clock(false);
	}
	k_mutex_unlock(&lock);
	if (hfxo_on) {
		onoff_release(hfxo);
		hfxo_on = false;
	}
	if (was) {
		LOG_INF("microphone off: %u frames, %u lost, %u events, encode %u us avg, %u max", stats.frames,
			stats.lost, stats.events, stats.encoded ? stats.encode_us / stats.encoded : 0,
			stats.encode_max_us);
	}
}

void mic_get_stats(struct mic_stats *out)
{
	*out = stats;
}

static int mic_init(void)
{
	IRQ_CONNECT(PDM0_IRQn, 5, pdm_isr, NULL, 0);
	enc = lc3_setup_encoder(10000, 16000, 0, &enc_mem);
	return 0;
}

SYS_INIT(mic_init, APPLICATION, 90);
