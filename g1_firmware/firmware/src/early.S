/* Reset entry: MCUboot (or the CPU) jumps here through the vector table, before Zephyr's
 * z_arm_reset. g1_early_wdt() feeds or starts nRF WDT0, then Zephyr starts as usual. */
	.syntax unified
	.thumb
	.section .text.g1_early, "ax", %progbits
	.global __wrap_z_arm_reset
	.type __wrap_z_arm_reset, %function
	.thumb_func
__wrap_z_arm_reset:
	bl g1_early_wdt
	b __real_z_arm_reset
