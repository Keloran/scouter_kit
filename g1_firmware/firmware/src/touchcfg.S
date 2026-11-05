/* The TouchBar controller's 44-entry configuration table ({u32 register, u32 value} little endian), the one
 * stock 1.6.6 loads into it (flash 0x8DB58), extracted from Even's image by scripts/fetch_stock.sh and checked
 * by its SHA-256 (stock_images.txt); src/touch.c writes it. */
	.section .rodata.g1_touch_table, "a"
	.align 4
	.global g1_touch_table
g1_touch_table:
	.incbin G1_TOUCH_TABLE_PATH
	.global g1_touch_table_end
g1_touch_table_end:
