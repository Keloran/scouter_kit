/* An Even net-core image (MCUboot-wrapped, B0N-signed by Even), embedded read-only so g1v0 can
 * reflash the net core by itself through the stock MCUboot multi-image/PCD path (src/netfallback.c).
 * Default: stock 1.6.6 (firmware-plan.md §3.3); CMake -DG1_NET_IMAGE=<net_core_app_update.bin>
 * -DG1_NET_IMAGE_NAME=<version> selects another one (e.g. fw/stock-1.5.6/, 1.5.6). g1v0 drives the 1.3.6, 1.5.6 and 1.6.6 net cores over IPC unchanged
 * (emulator.md §10.7). */
	.section .rodata.g1_net_image, "a"
	.align 4
	.global g1_net_image
g1_net_image:
	.incbin G1_NET_IMAGE_PATH
	.global g1_net_image_end
g1_net_image_end:
	.align 4
	.global g1_net_image_size
g1_net_image_size:
	.word g1_net_image_end - g1_net_image
