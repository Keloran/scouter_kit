/* The idle-current bisect (docs/power.md §15; test builds): switches set by DIAG SET 7 (a mask) or by the
 * phases of the net-off measurement, RAM only and all off at boot. Nothing else uses them. */
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/pm/device.h>
#include <zephyr/logging/log.h>
#include <hal/nrf_gpio.h>
#include "g1.h"

LOG_MODULE_REGISTER(idle, LOG_LEVEL_INF);

#define PIN0(p) NRF_GPIO_PIN_MAP(0, p)
#define TOUCH_CMD 0x4408

static uint8_t mask;

static void pin_float(uint32_t pin, nrf_gpio_pin_pull_t pull)
{
	nrf_gpio_cfg(pin, NRF_GPIO_PIN_DIR_INPUT, NRF_GPIO_PIN_INPUT_DISCONNECT, pull, NRF_GPIO_PIN_S0S1,
		     NRF_GPIO_PIN_NOSENSE);
}

static void pin_drive(uint32_t pin, bool high)
{
	if (high) {
		nrf_gpio_pin_set(pin);
	} else {
		nrf_gpio_pin_clear(pin);
	}
	nrf_gpio_cfg_output(pin);
}

static int apply(uint8_t bit, bool on)
{
	const struct device *twim2 = DEVICE_DT_GET_OR_NULL(DT_NODELABEL(i2c2));
	static const uint8_t lows[] = {19, 21, 23};
	int err = 0;

	switch (bit) {
	case IDLE_QSPI_DPD:     /* refused: on the arm the resume could not wake the flash (the QSPI's activation reads
				 * its status first), which then stayed asleep until a power cycle (power.md §15) */
		return -ENOTSUP;
	case IDLE_QSPI_PULLS:   /* the QSPI lines float while the flash is idle: SCK, IO0, IO1 down, IO2 (WP#), IO3 up */
		pin_float(PIN0(13), on ? NRF_GPIO_PIN_PULLDOWN : NRF_GPIO_PIN_NOPULL);
		pin_float(PIN0(14), on ? NRF_GPIO_PIN_PULLDOWN : NRF_GPIO_PIN_NOPULL);
		pin_float(PIN0(15), on ? NRF_GPIO_PIN_PULLUP : NRF_GPIO_PIN_NOPULL);
		pin_float(PIN0(16), on ? NRF_GPIO_PIN_PULLUP : NRF_GPIO_PIN_NOPULL);
		pin_float(PIN0(17), on ? NRF_GPIO_PIN_PULLDOWN : NRF_GPIO_PIN_NOPULL);
		break;
	case IDLE_UART_TX:      /* the console's TX (P0.20) not driven: the log output is lost meanwhile */
		if (on) {
			pin_float(PIN0(20), NRF_GPIO_PIN_NOPULL);
		} else {
			pin_drive(PIN0(20), true);
		}
		break;
	case IDLE_LOWS:         /* stock's P0.19, P0.21, P0.23 not driven low */
		for (int i = 0; i < ARRAY_SIZE(lows); i++) {
			if (on) {
				pin_float(PIN0(lows[i]), NRF_GPIO_PIN_NOPULL);
			} else {
				pin_drive(PIN0(lows[i]), false);
			}
		}
		break;
	case IDLE_PANEL_EN:     /* the panel's -2 V enable (P0.24) not driven, only while the panel is off */
		if (on && (panel_is_on() || panel_in_standby())) {
			return -EBUSY;
		}
		if (on) {
			pin_float(PIN0(24), NRF_GPIO_PIN_NOPULL);
		} else {
			pin_drive(PIN0(24), panel_is_on());
		}
		break;
	case IDLE_SENSORS:      /* the IMU powered down, the TouchBar controller in deep sleep */
		err = imu_set_rate(on ? 0 : 13);
		err |= touch_reg_write(TOUCH_CMD, on ? 3 : 2);
		break;
	case IDLE_TWIM2:        /* TWIM2 (IMU, TouchBar) disabled, its pins in their sleep state; the sensors fail meanwhile */
		err = twim2 ? pm_device_action_run(twim2, on ? PM_DEVICE_ACTION_SUSPEND : PM_DEVICE_ACTION_RESUME) : -ENODEV;
		break;
	}
	return err;
}

static void change(uint8_t bit, bool on)
{
	int err = apply(bit, on);

	if (err == 0) {
		mask ^= BIT(bit);
	}
	LOG_WRN("idle switch %u %s (%d)", bit, on ? "on" : "off", err);
}

/* the switches of `m` on, the others off (off from the last bit down, on from the first up: the sensors need
 * TWIM2); returns the mask in force */
uint8_t idle_set(uint8_t m)
{
	for (int bit = IDLE_BITS - 1; bit >= 0; bit--) {
		if ((mask & BIT(bit)) && !(m & BIT(bit))) {
			change(bit, false);
		}
	}
	for (int bit = 0; bit < IDLE_BITS; bit++) {
		if ((m & BIT(bit)) && !(mask & BIT(bit))) {
			change(bit, true);
		}
	}
	return mask;
}
