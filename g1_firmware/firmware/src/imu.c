/* LSM6DSO accelerometer and gyroscope (VM.md §2.5, §5.2, §6.6, §6.7) on TWIM2 at 0x6B, powered by the nPM1300's
 * LDSW2 with the microphone (switched on at boot as stock). ±2 g, BDU; low-power mode at 12.5, 26 and 52 Hz,
 * normal mode at 104 Hz; the gyroscope off unless GYRO_RATE asks for it (±500 dps). Readings are taken on
 * demand, so ACC_*, PITCH, ROLL and GYR_* are at most one sample period old. Only while MOTION or TILT can fire
 * (exec.c) is every
 * sample handled: then the chip batches its samples in the FIFO (continuous, BDR = IMU_RATE) and raises
 * INT1 (P1.15 on the G1, found by DIAG PIN_PROBE) at a watermark of one second of samples, or at once
 * when its wake-up function sees a slope above the threshold (at most MOTION_MG / (2 sqrt 3), so a pair
 * of samples further apart than MOTION_MG always wakes the CPU). The thread then handles every sample in
 * order: MOTION from consecutive samples, and each sample whose pitch is outside the TILT range is shown
 * to the executor on its own. So the CPU sleeps while the head is still. A board without INT1 polls
 * STATUS_REG a little faster than the ODR. */
#include <math.h>
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/gpio.h>
#include <zephyr/drivers/i2c.h>
#include <zephyr/logging/log.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(imu, LOG_LEVEL_INF);

#define ADDR 0x6B
#define FIFO_CTRL1 0x07
#define FIFO_CTRL2 0x08
#define FIFO_CTRL3 0x09
#define FIFO_CTRL4 0x0A
#define INT1_CTRL 0x0D
#define INT2_CTRL 0x0E
#define WHO_AM_I 0x0F
#define CTRL1_XL 0x10
#define CTRL2_G 0x11
#define CTRL3_C 0x12
#define CTRL6_C 0x15
#define CTRL7_G 0x16
#define STATUS_REG 0x1E
#define OUTX_L_G 0x22
#define OUTX_L_A 0x28
#define FIFO_STATUS1 0x3A
#define TAP_CFG0 0x56
#define TAP_CFG2 0x58
#define WAKE_UP_THS 0x5B
#define WAKE_UP_DUR 0x5C
#define MD1_CFG 0x5E
#define FREQ_FINE 0x63
#define FIFO_DATA_OUT_TAG 0x78
#define ID 0x6C
#define SW_RESET 0x01
#define BDU_IF_INC 0x44
#define XL_HM_MODE 0x10        /* CTRL6_C: low power at 12.5-52 Hz, normal mode at 104 Hz */
#define G_HM_MODE 0x80         /* CTRL7_G: the same for the gyroscope */
#define FS_G_500 0x04          /* CTRL2_G: ±500 dps, 17.5 mdps per LSB */
#define GYRO_ON_US 70000       /* the gyroscope's turn-on time from power-down */
#define XLDA 0x01
#define FIFO_CONTINUOUS 0x06
#define INT1_FIFO_TH 0x08
#define INT1_WU 0x20
#define INTERRUPTS_ENABLE 0x80
#define WAKE_THS_W 0x10        /* WAKE_UP_DUR: the threshold's LSB is FS/256 = 7.8125 mg at ±2 g */
#define TILT_THS 2             /* 15.6 mg of slope = 31 mg between samples, ~1.8 degrees */
#define TAG_XL 0x02
#define BATCH 128

/* Head frame (x right, y up, z toward the wearer; VM.md §2.5) from the sensor axes, Q14 (README "IMU axis map").
 * Provisional: X forward (stock's pitch is asin(-ax/|a|)), so z = -X, Y up on the right and down on the left (the
 * same board turned over), Z inward, tilted by stock's factory pitch base (settings +0x0C; G1_IMU_BASE_DEFAULT
 * where it is 0) so that its level pose reads 0. These axes are not a rotation (determinant -1, x mirrored); the
 * worn two-pose calibration (tools/imu_cal.py, 2026-11-01) below replaces them. */
static int16_t head_map[3][3];
static int8_t map_det = 1;   /* angular rates are axial vectors: a mirroring map flips their sign */

/* the worn two-pose result per arm (0 right, 1 left) as tools/imu_cal.py prints it; zero: provisional */
static const int16_t measured_map[2][3][3] = {
	{{4224, -185, 15829}, {5992, 15183, -1421}, {-14652, 6156, 3982}},   /* right */
	{{-5128, 185, -15560}, {5382, -15351, -1956}, {-14601, -5723, 4743}},   /* left */
};

static int8_t map_sign(void)
{
	const int16_t(*m)[3] = head_map;
	int64_t d = (int64_t)m[0][0] * (m[1][1] * m[2][2] - m[1][2] * m[2][1]) -
		    (int64_t)m[0][1] * (m[1][0] * m[2][2] - m[1][2] * m[2][0]) +
		    (int64_t)m[0][2] * (m[1][0] * m[2][1] - m[1][1] * m[2][0]);

	return d < 0 ? -1 : 1;
}

static void head_map_init(void)
{
	const int16_t(*mm)[3] = measured_map[board_role() ? 1 : 0];
	int32_t b = g1_settings.imu_base;

	if (mm[1][0] || mm[1][1] || mm[1][2]) {
		memcpy(head_map, mm, sizeof(head_map));
		map_det = map_sign();
		LOG_INF("head map: measured");
		return;
	}
	int32_t used = (b != IMU_BASE_NONE && b != 0 && b >= -4500 && b <= 4500) ? b : CONFIG_G1_IMU_BASE_DEFAULT;
	float u = board_role() ? -1.0f : 1.0f, e = -used * (3.14159265f / 18000.0f);
	float m[3][3] = {{0.0f, 0.0f, -u}, {sinf(e), u * cosf(e), 0.0f}, {-cosf(e), u * sinf(e), 0.0f}};

	for (int i = 0; i < 3; i++) {
		for (int j = 0; j < 3; j++) {
			head_map[i][j] = (int16_t)lroundf(m[i][j] * 16384.0f);
		}
	}
	map_det = map_sign();
	LOG_INF("head map (Q14) %d %d %d / %d %d %d / %d %d %d, factory pitch base %d", head_map[0][0],
		head_map[0][1], head_map[0][2], head_map[1][0], head_map[1][1], head_map[1][2], head_map[2][0],
		head_map[2][1], head_map[2][2], b);
}

void imu_map(int16_t out[9], int32_t *base)
{
	memcpy(out, head_map, sizeof(head_map));
	*base = g1_settings.imu_base;
}

struct sample {
	int32_t acc[3], pitch, roll;
};

#define USER DT_PATH(zephyr_user)
#if DT_NODE_HAS_STATUS(DT_NODELABEL(i2c2), okay)
static const struct device *bus = DEVICE_DT_GET(DT_NODELABEL(i2c2));
#else
static const struct device *bus;
#endif
#if DT_NODE_HAS_PROP(USER, imu_int1_gpios)
static const struct gpio_dt_spec int1 = GPIO_DT_SPEC_GET(USER, imu_int1_gpios);
static struct gpio_callback int1_cb;
#define HAVE_INT1 1
#else
#define HAVE_INT1 0
#endif
static K_MUTEX_DEFINE(lock);
static K_SEM_DEFINE(wake_sem, 0, 1);
static bool present, have_prev, batching, last_outside;
static volatile bool streaming;
static uint32_t rate = 13, period_us = 80000, ready_us, read_us;
static uint8_t wake_ths = TILT_THS;
static int8_t freq_fine;
static struct sample cache;
static bool valid;
static int32_t prev[3];
static uint32_t samples, misses, i2c_errors, irqs;
static uint32_t gyro_rate, gyro_period_us, gyro_ready_us, gyro_read_us;
static bool gyro_valid;
static int32_t gyro[3];
static struct sample batch[BATCH];

static int read_batch(int *words);
static bool outside_range(const struct sample *s);

static int wr(uint8_t reg, uint8_t v)
{
	int err = i2c_reg_write_byte(bus, ADDR, reg, v);

	i2c_errors += err != 0;
	return err;
}

static int rd(uint8_t reg, uint8_t *v, size_t n)
{
	int err = i2c_burst_read(bus, ADDR, reg, v, n);

	i2c_errors += err != 0;
	return err;
}

static uint8_t odr_code(uint32_t hz)
{
	switch (hz) {
	case 13:
		return 1;
	case 26:
		return 2;
	case 52:
		return 3;
	case 104:
		return 4;
	}
	return 0;
}

/* the chip's own ODR differs from the nominal one by INTERNAL_FREQ_FINE x 0.15 % */
static uint32_t period_of(uint32_t hz)
{
	uint32_t nominal = hz == 13 ? 80000 : hz == 26 ? 38462 : hz == 52 ? 19231 : 9615;

	return (uint32_t)((int64_t)nominal * 10000 / (10000 + 15 * freq_fine));
}

static int32_t deg(float y, float x)
{
	int32_t d = (int32_t)lroundf(atan2f(y, x) * (180.0f / 3.14159265f));

	return CLAMP(d, -90, 90);
}

/* six output bytes (sensor axes) -> the head frame */
static void convert(const uint8_t *r, struct sample *out)
{
	const int16_t(*m)[3] = head_map;
	int32_t s[3];

	for (int i = 0; i < 3; i++) {
		int32_t raw = (int16_t)(r[2 * i] | r[2 * i + 1] << 8);

		s[i] = (raw * 61 + (raw < 0 ? -500 : 500)) / 1000;
	}
	for (int i = 0; i < 3; i++) {
		int32_t v = m[i][0] * s[0] + m[i][1] * s[1] + m[i][2] * s[2];

		out->acc[i] = (v + (1 << 13)) >> 14;
	}
	out->pitch = deg(-out->acc[2], out->acc[1]);
	out->roll = deg(-out->acc[0], out->acc[1]);
}

/* the latest reading, for the properties; lock held */
static void publish(const struct sample *s)
{
	cache = *s;
	valid = true;
	read_us = now_us();
	samples++;
}

/* FIFO, wake-up function and INT1 routes, for every sample (on) or none (off); lock held */
static void batching_set(bool on)
{
	uint32_t wtm = rate;

	wr(INT1_CTRL, 0);
	wr(MD1_CFG, 0);
	wr(FIFO_CTRL4, 0);
	if (on && rate) {
		wr(FIFO_CTRL1, wtm & 0xFF);
		wr(FIFO_CTRL2, wtm >> 8);
		wr(FIFO_CTRL3, odr_code(rate));
		wr(FIFO_CTRL4, FIFO_CONTINUOUS);
		wr(WAKE_UP_DUR, WAKE_THS_W);
		wr(WAKE_UP_THS, wake_ths);
		wr(TAP_CFG0, 0);
		wr(TAP_CFG2, INTERRUPTS_ENABLE);
		wr(INT1_CTRL, INT1_FIFO_TH);
		wr(MD1_CFG, INT1_WU);
	} else {
		wr(TAP_CFG2, 0);
	}
	batching = on && rate;
	have_prev = false;
	last_outside = false;
}

static int set_rate_locked(uint32_t hz)
{
	int err = wr(CTRL1_XL, odr_code(hz) << 4);

	if (!err) {
		rate = hz;
		period_us = hz ? period_of(hz) : 0;
		ready_us = now_us() + 2 * period_us;
		valid = false;
		have_prev = false;
		if (batching) {
			batching_set(true);
		}
	}
	return err;
}

int imu_set_rate(uint32_t hz)
{
	int err;

	if (!present || hz == rate) {
		rate = hz;
		return present ? 0 : -ENODEV;
	}
	k_mutex_lock(&lock, K_FOREVER);
	err = set_rate_locked(hz);
	k_mutex_unlock(&lock);
	LOG_INF("IMU_RATE %u Hz%s", hz, err ? " (I2C error)" : "");
	k_sem_give(&wake_sem);
	return err;
}

bool imu_present(void)
{
	return present;
}

/* GYRO_RATE (VM.md §6.7): the gyroscope at 13, 26, 52 or 104 Hz (low power below 104), ±500 dps; 0 off */
int imu_set_gyro_rate(uint32_t hz)
{
	int err;

	if (!present) {
		return -ENODEV;
	}
	k_mutex_lock(&lock, K_FOREVER);
	err = wr(CTRL7_G, G_HM_MODE) | wr(CTRL2_G, hz ? odr_code(hz) << 4 | FS_G_500 : 0);
	if (!err) {
		gyro_rate = hz;
		gyro_period_us = hz ? period_of(hz) : 0;
		gyro_ready_us = now_us() + GYRO_ON_US + 2 * gyro_period_us;
		gyro_valid = false;
	}
	k_mutex_unlock(&lock);
	LOG_INF("GYRO_RATE %u Hz%s", hz, err ? " (I2C error)" : "");
	return err;
}

uint32_t imu_gyro_rate(void)
{
	return present ? gyro_rate : VM_UNKNOWN;
}

/* GYR_X/Y/Z, mdps in the head frame: the latest output, read now if it is older than one period */
uint32_t imu_gyro_prop(uint32_t id)
{
	uint32_t v = VM_UNKNOWN;
	uint8_t r[6];

	if (!present || !gyro_rate) {
		return VM_UNKNOWN;
	}
	k_mutex_lock(&lock, K_FOREVER);
	if ((int32_t)(now_us() - gyro_ready_us) >= 0 && (!gyro_valid || now_us() - gyro_read_us >= gyro_period_us) &&
	    rd(OUTX_L_G, r, 6) == 0) {
		int32_t s[3];

		for (int i = 0; i < 3; i++) {
			int32_t raw = (int16_t)(r[2 * i] | r[2 * i + 1] << 8);

			s[i] = (raw * 35 + (raw < 0 ? -1 : 1)) / 2;
		}
		for (int i = 0; i < 3; i++) {
			int64_t g = (int64_t)head_map[i][0] * s[0] + (int64_t)head_map[i][1] * s[1] + (int64_t)head_map[i][2] * s[2];

			gyro[i] = map_det * (int32_t)((g + (1 << 13)) >> 14);
		}
		gyro_valid = true;
		gyro_read_us = now_us();
	}
	if (gyro_valid) {
		v = (uint32_t)gyro[id - VM_P_GYR_X];
	}
	k_mutex_unlock(&lock);
	return v;
}

/* ACC_X/Y/Z (mg), PITCH, ROLL (degrees): the latest sample, read now if it is older than one period. While
 * the FIFO batches, it is drained here (in order, MOTION included), so a reading never runs ahead of the
 * samples still to be handled; while polling, the thread's own reads keep it fresh (a read here would take
 * STATUS_REG's flag from the thread). */
uint32_t imu_prop(uint32_t id)
{
	uint32_t v = VM_UNKNOWN;
	struct sample s;
	uint8_t r[6];
	int words, k;

	if (!present || !rate) {
		return VM_UNKNOWN;
	}
	k_mutex_lock(&lock, K_FOREVER);
	if (batching && (!valid || now_us() - read_us >= period_us)) {
		/* the FIFO's newest sample, so a reading never runs ahead of the samples still to be handled */
		do {
			k = read_batch(&words);
			for (int i = 0; i < k; i++) {
				last_outside = outside_range(&batch[i]);
			}
			if (k) {
				publish(&batch[k - 1]);
			}
		} while (words > 0 && k > 0);
	} else if (!streaming && (int32_t)(now_us() - ready_us) >= 0 &&
		   (!valid || now_us() - read_us >= period_us) && rd(OUTX_L_A, r, 6) == 0) {
		convert(r, &s);
		publish(&s);
	}
	if (valid) {
		switch (id) {
		case VM_P_ACC_X:
		case VM_P_ACC_Y:
		case VM_P_ACC_Z:
			v = (uint32_t)cache.acc[id - VM_P_ACC_X];
			break;
		case VM_P_PITCH:
			v = (uint32_t)cache.pitch;
			break;
		case VM_P_ROLL:
			v = (uint32_t)cache.roll;
			break;
		}
	}
	k_mutex_unlock(&lock);
	return v;
}

/* exec.c: MOTION or TILT can fire, so every sample is needed; motion = MOTION_MG while MOTION can fire,
 * else 0. The wake-up threshold is the TILT step, or less for a small MOTION_MG. */
void imu_stream(bool on, uint32_t motion)
{
	uint32_t ths = TILT_THS;

	if (motion) {
		ths = MIN(ths, (uint32_t)((uint64_t)motion * 2887 / 78125));
	}
	if (on != streaming || (on && ths != wake_ths)) {
		k_mutex_lock(&lock, K_FOREVER);
		wake_ths = (uint8_t)ths;
		if (on && batching) {
			wr(WAKE_UP_THS, wake_ths);
		}
		streaming = on;
		if (!on) {
			have_prev = false;
		}
		k_mutex_unlock(&lock);
		k_sem_give(&wake_sem);
	}
}

void imu_stats(uint32_t *n, uint32_t *missed, uint32_t *errors, bool *on)
{
	*n = samples;
	*missed = misses;
	*errors = i2c_errors;
	*on = streaming;
}

uint32_t imu_irqs(void)
{
	return irqs;
}

/* MOTION, in order, against the sample before (lock held); in the case, a lift out of it (VM.md §2.5) */
static void motion_step(const struct sample *s)
{
	if (have_prev && vm_moved(prev, s->acc, exec_motion_mg())) {
		exec_trigger(VM_T_MOTION, vm_distance(prev, s->acc));
	}
	if (have_prev && g1_in_case() && vm_moved(prev, s->acc, LIFT_MG)) {
		g1_case_moved();
	}
	prev[0] = s->acc[0], prev[1] = s->acc[1], prev[2] = s->acc[2];
	have_prev = true;
}

/* whether a sample is outside the TILT range (read now: a handler may have moved it) */
static bool outside_range(const struct sample *s)
{
	int32_t lo, hi;

	exec_pitch_range(&lo, &hi);
	return lo > hi || s->pitch < lo || s->pitch > hi;
}

/* The executor evaluates the range when it goes idle, so a sample outside it, and the first one back
 * inside, are published one at a time; the imu thread (above the executor) then waits 1 ms. */
static void show(const struct sample *s, bool wait)
{
	int32_t old;

	k_mutex_lock(&lock, K_FOREVER);
	old = valid ? cache.pitch : INT32_MIN;
	publish(s);
	k_mutex_unlock(&lock);
	if (s->pitch != old) {
		exec_wake();
		if (wait) {
			k_msleep(1);
		}
	}
}

#if !HAVE_INT1
static void feed(const struct sample *s, bool last)
{
	bool outside;

	k_mutex_lock(&lock, K_FOREVER);
	motion_step(s);
	k_mutex_unlock(&lock);
	outside = outside_range(s);
	if (outside || last_outside || last) {
		show(s, !last);
	}
	last_outside = outside;
}
#endif

#if HAVE_INT1
static void int1_isr(const struct device *d, struct gpio_callback *cb, uint32_t pins)
{
	gpio_pin_interrupt_configure_dt(&int1, GPIO_INT_DISABLE);
	irqs++;
	k_sem_give(&wake_sem);
}

#endif

/* Up to BATCH FIFO words into batch[] with MOTION done in order (lock held); *words: what is left. */
static int read_batch(int *words)
{
	uint8_t st[2], w[7];
	int k = 0;

	*words = 0;
	if (!batching || rd(FIFO_STATUS1, st, 2) != 0) {
		return 0;
	}
	*words = st[0] | (st[1] & 3) << 8;
	if (st[1] & 0x40) {
		have_prev = false;
	}
	while (*words > 0 && k < BATCH && rd(FIFO_DATA_OUT_TAG, w, 7) == 0) {
		(*words)--;
		if ((w[0] >> 3) == TAG_XL) {
			convert(&w[1], &batch[k]);
			motion_step(&batch[k++]);
		}
	}
	return k;
}

#if HAVE_INT1
/* everything the FIFO holds, in order: the imu thread shows the samples the TILT range needs one by one */
static void drain_fifo(void)
{
	int words, k;

	do {
		k_mutex_lock(&lock, K_FOREVER);
		k = read_batch(&words);
		k_mutex_unlock(&lock);
		for (int i = 0; i < k; i++) {
			bool last = i == k - 1 && words == 0, outside = outside_range(&batch[i]);

			if (outside || last_outside || last) {
				show(&batch[i], !last);
			}
			last_outside = outside;
		}
	} while (words > 0 && k > 0);
}
#endif

#if !HAVE_INT1
static void poll_once(void)
{
	uint8_t r[16];
	struct sample s;
	bool got = false;

	k_mutex_lock(&lock, K_FOREVER);
	if ((int32_t)(now_us() - ready_us) >= 0 && rd(STATUS_REG, r, sizeof(r)) == 0 && (r[0] & XLDA)) {
		convert(&r[OUTX_L_A - STATUS_REG], &s);
		got = true;
	} else {
		misses++;
	}
	k_mutex_unlock(&lock);
	if (got) {
		feed(&s, true);
	}
}
#endif

static void imu_thread(void *a, void *b, void *c)
{
	for (;;) {
		while (!streaming || !present || !rate) {
#if HAVE_INT1
			if (batching && present) {
				k_mutex_lock(&lock, K_FOREVER);
				batching_set(false);
				k_mutex_unlock(&lock);
				gpio_pin_interrupt_configure_dt(&int1, GPIO_INT_DISABLE);
			}
#endif
			k_sem_take(&wake_sem, K_FOREVER);
		}
#if HAVE_INT1
		if (!batching) {
			k_mutex_lock(&lock, K_FOREVER);
			batching_set(true);
			k_mutex_unlock(&lock);
		}
		/* an edge interrupt (a GPIOTE IN channel, only while streaming); a line that is already high
		 * has no edge left, so it is handled at once */
		gpio_pin_interrupt_configure_dt(&int1, GPIO_INT_EDGE_TO_ACTIVE);
		if (gpio_pin_get_dt(&int1) == 1) {
			k_sem_give(&wake_sem);
		}
		k_sem_take(&wake_sem, K_SECONDS(2));
		if (!streaming || !present || !rate) {
			continue;
		}
		drain_fifo();
		/* while the wake-up line stays active (the head moves), once per sample period */
		k_sem_take(&wake_sem, K_USEC(period_us));
#else
		uint32_t t0 = now_us(), wait = period_us * 95 / 100;

		poll_once();
		wait -= MIN(now_us() - t0, wait);
		k_sem_take(&wake_sem, K_USEC(wait));
#endif
	}
}

K_THREAD_DEFINE(imu, 1280, imu_thread, NULL, NULL, NULL, 7, 0, 0);

/* LDSW2 went off (a power measurement): no readings until imu_init */
void imu_off(void)
{
	k_mutex_lock(&lock, K_FOREVER);
	present = false;
	valid = false;
	batching = false;
	k_mutex_unlock(&lock);
#if HAVE_INT1
	gpio_pin_interrupt_configure_dt(&int1, GPIO_INT_DISABLE);
#endif
}

/* DIAG PIN_PROBE (diag.c): which nRF pins the INT1/INT2 lines reach. Open drain and active low, so the
 * chip only ever pulls a line low and the probe's pull-ups do the rest; the data-ready signal (latched,
 * the data is not read meanwhile) makes the routed line active. The lock is held from the first call
 * to the restore (0xFF), so the imu thread does not read the data. */
#define H_LACTIVE_PP_OD 0x30

int imu_int_drive(uint8_t state)
{
	static bool held;
	int err = 0;

	if (!present) {
		return -ENODEV;
	}
	if (state == 0xFF) {
		if (held) {
			err |= wr(INT1_CTRL, 0);
			err |= wr(INT2_CTRL, 0);
			err |= wr(CTRL3_C, BDU_IF_INC);
			err |= set_rate_locked(rate);
			if (batching) {
				batching_set(true);
			}
			held = false;
			k_mutex_unlock(&lock);
		}
		return err ? -EIO : 0;
	}
	if (!held) {
		k_mutex_lock(&lock, K_FOREVER);
		held = true;
#if HAVE_INT1
		gpio_pin_interrupt_configure_dt(&int1, GPIO_INT_DISABLE);
#endif
		err |= wr(MD1_CFG, 0);
		err |= wr(CTRL3_C, BDU_IF_INC | H_LACTIVE_PP_OD);
		err |= wr(CTRL1_XL, odr_code(104) << 4);
	}
	err |= wr(INT1_CTRL, state == 1 ? 1 : 0);
	err |= wr(INT2_CTRL, state == 2 ? 1 : 0);
	k_msleep(25);
	return err ? -EIO : 0;
}

/* After LDSW2 is on (PMIC thread): the registers as an earlier image left them (POWER telemetry; the
 * right arm's ran at 12.5 Hz high-performance under stock), a software reset, then the boot state
 * of VM.md §6.6: accelerometer at IMU_RATE (13 Hz) in low-power mode, gyroscope off. */
void imu_init(void)
{
	uint8_t id = 0xFF, xl = 0xFF, g = 0xFF, c3 = SW_RESET, ff = 0;

	head_map_init();
	if (!bus || !device_is_ready(bus) || rd(WHO_AM_I, &id, 1) != 0 || id != ID) {
		LOG_WRN("LSM6DSO not answering (id 0x%02x; LDSW2 off?)", id);
		present = false;
		return;
	}
	rd(CTRL1_XL, &xl, 1);
	rd(CTRL2_G, &g, 1);
	LOG_INF("LSM6DSO id 0x%02x, CTRL1_XL 0x%02x, CTRL2_G 0x%02x as found", id, xl, g);
	power_imu_regs[0] = id;
#if HAVE_INT1
	static bool cb_added;

	if (!cb_added) {
		gpio_pin_configure_dt(&int1, GPIO_INPUT);
		gpio_init_callback(&int1_cb, int1_isr, BIT(int1.pin));
		gpio_add_callback(int1.port, &int1_cb);
		cb_added = true;
	}
#endif
	k_mutex_lock(&lock, K_FOREVER);
	wr(CTRL3_C, SW_RESET);
	for (int i = 0; i < 10 && (c3 & SW_RESET); i++) {
		k_msleep(1);
		rd(CTRL3_C, &c3, 1);
	}
	rd(FREQ_FINE, &ff, 1);
	freq_fine = (int8_t)ff;
	batching = false;
	gyro_rate = 0;
	present = !(c3 & SW_RESET) && wr(CTRL3_C, BDU_IF_INC) == 0 && wr(CTRL6_C, XL_HM_MODE) == 0 &&
		  wr(CTRL2_G, 0) == 0 && set_rate_locked(rate) == 0;
	rd(CTRL1_XL, &xl, 1);
	rd(CTRL2_G, &g, 1);
	k_mutex_unlock(&lock);
	power_imu_regs[1] = xl;
	power_imu_regs[2] = g;
	power_imu_configured = present;
	LOG_INF("LSM6DSO %s: CTRL1_XL 0x%02x, CTRL2_G 0x%02x, %u Hz low power, freq fine %d, INT1 %s",
		present ? "set" : "NOT set", xl, g, rate, freq_fine, HAVE_INT1 ? "P1.15" : "none (polling)");
	k_sem_give(&wake_sem);
}
