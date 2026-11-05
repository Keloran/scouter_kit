/* Retained RAM (pm partition g1_retained, top 8 KiB of app RAM, not used by MCUboot):
 * boot accounting for safe mode and the net fallback, the last fault, and a text log ring. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/sys/crc.h>
#include <zephyr/logging/log.h>
#include <zephyr/logging/log_backend.h>
#include <zephyr/logging/log_output.h>
#include <pm_config.h>
#include <nrf.h>
#include "g1.h"

#define RET_MAGIC 0x47315630u
#define LOG_MAGIC 0x4C4F4731u
#define STATE_RUNNING 1
#define STATE_CLEAN 2

struct retained {
	uint32_t magic;
	uint32_t boot_count;
	uint32_t resetreas;
	uint8_t unhealthy;
	uint8_t last_state;
	uint8_t clean_kind;
	uint8_t force_safe;
	uint32_t fault_count;
	uint32_t fault_reason, fault_pc, fault_lr, fault_uptime_ms;
	char fault_thread[16];
	uint32_t health_reason;
	uint8_t net_fails;
	uint8_t net_fallback_done;
	uint32_t crc;
};

struct logring {
	uint32_t magic;
	uint32_t head;
	uint32_t wrapped;
	uint8_t data[PM_G1_RETAINED_SIZE - 512 - 12];
};

/* Beside the boot record, with a magic of its own (so 0.5.0's record stays valid): the case link's
 * "full" latch, kept over soft resets, and the net-off measurement's request and results (diag.c). */
#define EXTRA_MAGIC 0x47315832u
struct ret_extra {
	uint32_t magic;
	uint8_t case_full;
	uint8_t test_pending, test_phases, test_done;
	uint16_t test_seconds;
	uint8_t test_net_fails;
	struct netoff_phase result[NETOFF_PHASES];
	uint8_t test_masks[NETOFF_PHASES];
	uint8_t test_masked;
};

BUILD_ASSERT(sizeof(struct retained) <= 256, "retained block too big");
BUILD_ASSERT(256 + sizeof(struct ret_extra) <= 512, "extra block too big");

#define RET ((struct retained *)PM_G1_RETAINED_ADDRESS)
#define EXTRA ((struct ret_extra *)(PM_G1_RETAINED_ADDRESS + 256))
#define RING ((struct logring *)(PM_G1_RETAINED_ADDRESS + 512))

static uint8_t level;

static uint32_t ret_crc(void)
{
	return crc32_ieee((const uint8_t *)RET, offsetof(struct retained, crc));
}

static void ret_save(void)
{
	RET->crc = ret_crc();
}

static void ring_write(const uint8_t *d, size_t n)
{
	if (RING->magic != LOG_MAGIC || RING->head >= sizeof(RING->data)) {
		RING->magic = LOG_MAGIC;
		RING->head = 0;
		RING->wrapped = 0;
	}
	for (size_t i = 0; i < n; i++) {
		RING->data[RING->head++] = d[i];
		if (RING->head >= sizeof(RING->data)) {
			RING->head = 0;
			RING->wrapped = 1;
		}
	}
}

size_t retained_log_read(uint32_t off, uint8_t *out, size_t max, uint32_t *total)
{
	uint32_t size = RING->wrapped ? sizeof(RING->data) : RING->head;
	uint32_t start = RING->wrapped ? RING->head : 0;
	size_t n = 0;

	*total = size;
	while (off < size && n < max) {
		out[n++] = RING->data[(start + off) % sizeof(RING->data)];
		off++;
	}
	return n;
}

void retained_boot(void)
{
	uint32_t rr = NRF_RESET_S->RESETREAS;

	NRF_RESET_S->RESETREAS = rr;
	if (RET->magic != RET_MAGIC || RET->crc != ret_crc()) {
		memset(RET, 0, sizeof(*RET));
		RET->magic = RET_MAGIC;
	} else if (RET->last_state == STATE_RUNNING) {
		if (RET->unhealthy < 255) {
			RET->unhealthy++;
		}
	} else if (RET->clean_kind == CLEAN_SMP) {
		RET->unhealthy = 0;
	}
	if (RING->magic != LOG_MAGIC || RING->head >= sizeof(RING->data)) {
		RING->magic = LOG_MAGIC;
		RING->head = 0;
		RING->wrapped = 0;
	}
	if (EXTRA->magic != EXTRA_MAGIC) {
		memset(EXTRA, 0, sizeof(*EXTRA));
		EXTRA->magic = EXTRA_MAGIC;
	}
	RET->boot_count++;
	RET->resetreas = rr;
	RET->last_state = STATE_RUNNING;
	RET->clean_kind = CLEAN_NONE;
	if (RET->force_safe) {
		level = 1;
	} else if (RET->unhealthy >= 6) {
		level = 2;
	} else if (RET->unhealthy >= 3) {
		level = 1;
	}
	RET->force_safe = 0;
	ret_save();
}

void retained_mark_clean(enum clean_kind kind, bool clear_counter, bool force_safe)
{
	RET->last_state = STATE_CLEAN;
	RET->clean_kind = kind;
	if (clear_counter) {
		RET->unhealthy = 0;
	}
	if (force_safe) {
		RET->force_safe = 1;
	}
	ret_save();
}

void retained_clear_unhealthy(void)
{
	RET->unhealthy = 0;
	ret_save();
}

void retained_record_fault(uint32_t reason, uint32_t pc, uint32_t lr)
{
	const char *name = k_thread_name_get(k_current_get());

	RET->fault_count++;
	RET->fault_reason = reason;
	RET->fault_pc = pc;
	RET->fault_lr = lr;
	RET->fault_uptime_ms = k_uptime_get_32();
	memset(RET->fault_thread, 0, sizeof(RET->fault_thread));
	if (name) {
		strncpy(RET->fault_thread, name, sizeof(RET->fault_thread) - 1);
	}
	ret_save();
}

void retained_set_health_reason(uint32_t reason)
{
	RET->health_reason = reason;
	ret_save();
}

uint8_t retained_net_fails(void) { return RET->net_fails; }
void retained_set_net_fails(uint8_t v) { RET->net_fails = v; ret_save(); }
bool retained_net_fallback_done(void) { return RET->net_fallback_done; }
void retained_set_net_fallback_done(void) { RET->net_fallback_done = 1; ret_save(); }

bool retained_case_full(void) { return EXTRA->case_full; }
void retained_set_case_full(bool full) { EXTRA->case_full = full; }

void retained_netoff_request(uint16_t seconds, uint8_t phases, const uint8_t *masks)
{
	memset(EXTRA->result, 0, sizeof(EXTRA->result));
	EXTRA->test_seconds = seconds;
	EXTRA->test_phases = MIN(phases, NETOFF_PHASES);
	EXTRA->test_masked = masks != NULL;
	if (masks) {
		memcpy(EXTRA->test_masks, masks, EXTRA->test_phases);
	}
	EXTRA->test_done = 0;
	EXTRA->test_pending = 1;
}

bool retained_netoff_take(uint16_t *seconds, uint8_t *phases, uint8_t *masks, bool *masked)
{
	if (!EXTRA->test_pending) {
		return false;
	}
	EXTRA->test_pending = 0;
	EXTRA->test_net_fails = RET->net_fails;
	*seconds = EXTRA->test_seconds;
	*phases = MIN(EXTRA->test_phases, NETOFF_PHASES);
	*masked = EXTRA->test_masked;
	memcpy(masks, EXTRA->test_masks, NETOFF_PHASES);
	return true;
}

void retained_netoff_store(const struct netoff_phase *p, uint8_t phases)
{
	memcpy(EXTRA->result, p, sizeof(EXTRA->result));
	EXTRA->test_done = phases;
}

uint8_t retained_netoff_result(struct netoff_phase *p, uint16_t *seconds, uint8_t *net_fails_at_test)
{
	memcpy(p, EXTRA->result, sizeof(EXTRA->result));
	*seconds = EXTRA->test_seconds;
	*net_fails_at_test = EXTRA->test_net_fails;
	return EXTRA->test_done;
}

uint32_t retained_boot_count(void) { return RET->boot_count; }
uint32_t retained_resetreas(void) { return RET->resetreas; }
uint8_t retained_unhealthy(void) { return RET->unhealthy; }
uint8_t safe_level(void) { return level; }

void retained_log_boot_banner(void);

void retained_log_boot_banner(void)
{
	char line[160];
	int n = snprintk(line, sizeof(line),
			 "\n=== boot %u resetreas 0x%08x unhealthy %u safe %u last fault #%u reason %u pc 0x%08x"
			 " lr 0x%08x at %u ms in %s, health reason %u\n",
			 RET->boot_count, RET->resetreas, RET->unhealthy, level, RET->fault_count,
			 RET->fault_reason, RET->fault_pc, RET->fault_lr, RET->fault_uptime_ms,
			 RET->fault_thread, RET->health_reason);

	ring_write((const uint8_t *)line, MIN(n, (int)sizeof(line) - 1));
}

/* Log backend that formats every message into the retained ring. */
static uint8_t lo_buf[96];

static int ring_out(uint8_t *data, size_t length, void *ctx)
{
	ring_write(data, length);
	return length;
}

LOG_OUTPUT_DEFINE(ring_output, ring_out, lo_buf, sizeof(lo_buf));

static void ring_process(const struct log_backend *const backend, union log_msg_generic *msg)
{
	log_output_msg_process(&ring_output, &msg->log, LOG_OUTPUT_FLAG_LEVEL | LOG_OUTPUT_FLAG_TIMESTAMP);
}

static void ring_panic(const struct log_backend *const backend)
{
	log_output_flush(&ring_output);
}

static void ring_dropped(const struct log_backend *const backend, uint32_t cnt)
{
	log_output_dropped_process(&ring_output, cnt);
}

static const struct log_backend_api ring_api = {
	.process = ring_process,
	.panic = ring_panic,
	.dropped = ring_dropped,
};

LOG_BACKEND_DEFINE(g1_ring_backend, ring_api, true);
