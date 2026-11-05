/* Board GPIOs (stock baseline from button_init) and the factory settings record on QSPI
 * (0x130000, read only; stock's load_sys_setting): first_enter_flag +0x00, ESB channel +0x08,
 * IMU pitch base +0x0C, panel voltage flag +0x14, frame serial +0x15, board SN +0x35, ESB addresses
 * +0x55/+0x56, luminance base +0x78. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/drivers/gpio.h>
#include <zephyr/drivers/flash.h>
#include <zephyr/logging/log.h>
#include <zephyr/init.h>
#include <hal/nrf_gpio.h>
#include <hal/nrf_uarte.h>
#include <soc.h>
#include "g1.h"

LOG_MODULE_REGISTER(board, LOG_LEVEL_INF);

#define USER DT_PATH(zephyr_user)
#define SETTINGS_ADDR 0x130000

struct g1_settings g1_settings;
static const struct gpio_dt_spec panel_en = GPIO_DT_SPEC_GET(USER, panel_en_gpios);
static const struct gpio_dt_spec role_pin = GPIO_DT_SPEC_GET(USER, role_gpios);
#if DT_NODE_HAS_PROP(USER, board_low_gpios)
static const struct gpio_dt_spec lows[] = {
	DT_FOREACH_PROP_ELEM_SEP(USER, board_low_gpios, GPIO_DT_SPEC_GET_BY_IDX, (,))
};
#endif
#if DT_NODE_HAS_PROP(USER, touch_power_gpios)
static const struct gpio_dt_spec touch_power = GPIO_DT_SPEC_GET(USER, touch_power_gpios);
#endif
static uint8_t role;

/* Before any driver starts (PRE_KERNEL_1, priority 0):
 * - The panel's MISO (P0.10) and CS (P0.11) must belong to the app core. Images before 0.3.4 handed them
 *   to the network core (the DK board's GPIO forwarder), and PIN_CNF.MCUSEL survives soft resets
 *   (checked on the DK), so an arm updated from such an image keeps them there until a pin reset or
 *   power cycle: take them back explicitly.
 * - Stock's UART0 has TX/RX only (PSEL.RTS/CTS disconnected). A bootloader can leave RTS/CTS connected
 *   (the DK's replica MCUboot leaves P0.19/P0.21; images up to 0.3.6 connected them themselves), and a
 *   connected RTS is driven by the UARTE even with flow control off (DK, 0.3.8 dev: P0.19 held low with
 *   OUT = 1 until UARTE0 was disabled). The UART driver only writes TX/RX, so disconnect both here.
 * - The board baseline levels (stock's button_init) at once, not only from main(). */
#define UART0_REGS ((NRF_UARTE_Type *)DT_REG_ADDR(DT_NODELABEL(uart0)))
#define EARLY_PIN(node, prop, idx) NRF_DT_GPIOS_TO_PSEL_BY_IDX(node, prop, idx),

static int board_early(void)
{
	nrf_gpio_pin_control_select(NRF_GPIO_PIN_MAP(0, 10), NRF_GPIO_PIN_SEL_APP);
	nrf_gpio_pin_control_select(NRF_GPIO_PIN_MAP(0, 11), NRF_GPIO_PIN_SEL_APP);
	nrf_uarte_task_trigger(UART0_REGS, NRF_UARTE_TASK_STOPRX);
	nrf_uarte_task_trigger(UART0_REGS, NRF_UARTE_TASK_STOPTX);
	k_busy_wait(200);
	nrf_uarte_disable(UART0_REGS);
	nrf_uarte_hwfc_pins_disconnect(UART0_REGS);
#if DT_NODE_HAS_PROP(USER, board_low_gpios)
	static const uint8_t low[] = {DT_FOREACH_PROP_ELEM(USER, board_low_gpios, EARLY_PIN)};

	for (int i = 0; i < ARRAY_SIZE(low); i++) {
		nrf_gpio_pin_clear(low[i]);
		nrf_gpio_cfg_output(low[i]);
	}
#endif
#if DT_NODE_HAS_PROP(USER, touch_power_gpios)
	uint32_t touch = NRF_DT_GPIOS_TO_PSEL(USER, touch_power_gpios);

	nrf_gpio_pin_set(touch);
	nrf_gpio_cfg_output(touch);
#endif
	return 0;
}
SYS_INIT(board_early, PRE_KERNEL_1, 0);

uint32_t now_us(void)
{
	return (uint32_t)k_ticks_to_us_floor64(k_uptime_ticks());
}

uint8_t board_role(void)
{
	return role;
}

void board_panel_enable(bool on)
{
	gpio_pin_set_dt(&panel_en, on);
}

/* the TouchBar controller's rail (P0.30), on from boot as stock; off only for a power measurement */
static bool touch_on = DT_NODE_HAS_PROP(USER, touch_power_gpios);

void board_touch_power(bool on)
{
#if DT_NODE_HAS_PROP(USER, touch_power_gpios)
	gpio_pin_set_dt(&touch_power, on);
	touch_on = on;
#endif
}

bool board_touch_powered(void)
{
	return touch_on;
}

/* Printable ASCII up to 16 bytes, ended by 0x00, 0xFF or the field's end; anything else is no serial. */
static void read_serial(const uint8_t *p)
{
	int n = 0;

	while (n < 16 && p[n] >= 0x21 && p[n] <= 0x7E) {
		n++;
	}
	if (n == 16 || p[n] == 0x00 || p[n] == 0xFF) {
		memcpy(g1_settings.serial, p, n);
	}
}

static void read_settings(void)
{
	const struct device *nor = DEVICE_DT_GET(DT_NODELABEL(mx25r64));
	uint8_t rec[0x80];

	g1_settings.esb_channel = 34;
	g1_settings.esb_master = 0x41;
	g1_settings.esb_slave = 0x42;
	g1_settings.lum_base = 63;
	g1_settings.imu_base = IMU_BASE_NONE;
	memset(g1_settings.sn, 0xFF, sizeof(g1_settings.sn));
	if (!device_is_ready(nor) || flash_read(nor, SETTINGS_ADDR, rec, sizeof(rec)) != 0) {
		LOG_WRN("settings record unreadable, defaults");
		return;
	}
	g1_settings.read_ok = true;
	memcpy(g1_settings.sn, &rec[0x35], sizeof(g1_settings.sn));
	read_serial(&rec[0x15]);
	LOG_INF("frame serial %s", g1_settings.serial[0] ? g1_settings.serial : "(none)");
	if (rec[0x08] != 0xFF) {
		g1_settings.esb_channel = rec[0x08];
	}
	if (rec[0x55] != 0xFF) {
		g1_settings.esb_master = rec[0x55];
	}
	if (rec[0x56] != 0xFF) {
		g1_settings.esb_slave = rec[0x56];
	}
	/* stock: a record without first_enter_flag 1 gets defaults (base 63, panel voltage flag 0);
	 * a base outside 1..63 becomes 63 */
	g1_settings.initialised = get32(&rec[0x00]) == 1;
	if (g1_settings.initialised) {
		uint16_t base = get16(&rec[0x78]);

		g1_settings.panel_v = rec[0x14];
		g1_settings.lum_base = (base >= 1 && base <= 63) ? base : 63;
		g1_settings.imu_base = (int32_t)get32(&rec[0x0C]);
	}
	LOG_INF("settings: esb channel %u master 0x%02x slave 0x%02x; first_enter %u, panel voltage flag %u, "
		"luminance base %u (raw %02x%02x), imu pitch base %d", g1_settings.esb_channel, g1_settings.esb_master,
		g1_settings.esb_slave, g1_settings.initialised, rec[0x14], g1_settings.lum_base, rec[0x79], rec[0x78],
		g1_settings.imu_base);
}

int board_init(void)
{
	gpio_pin_configure_dt(&role_pin, GPIO_INPUT);
	role = gpio_pin_get_dt(&role_pin) ? 1 : 0;
	gpio_pin_configure_dt(&panel_en, GPIO_OUTPUT_INACTIVE);
#if DT_NODE_HAS_PROP(USER, board_low_gpios)
	for (int i = 0; i < ARRAY_SIZE(lows); i++) {
		gpio_pin_configure_dt(&lows[i], GPIO_OUTPUT_INACTIVE);
	}
#endif
#if DT_NODE_HAS_PROP(USER, touch_power_gpios)
	gpio_pin_configure_dt(&touch_power, GPIO_OUTPUT_ACTIVE);
#endif
	read_settings();
	LOG_INF("role %s", role ? "left (slave)" : "right (master)");
	return 0;
}
