/* The asset store (VM.md §6.10, docs/design-flash-assets.md): host-named blobs in the g1_assets partition of the
 * QSPI flash. Only the store thread erases and programs, one bounded flash call at a time; the control thread
 * answers from the RAM directory; the executor's ASSET_LOAD only reads. Every access is a 24-bit driver call
 * inside the partition: the store sends no instruction of its own and leaves the chip's state as it is. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/logging/log.h>
#include <zephyr/logging/log_ctrl.h>
#include <zephyr/drivers/flash.h>
#include <zephyr/storage/flash_map.h>
#include <zephyr/sys/crc.h>
#include <zephyr/sys/reboot.h>
#include <tinycrypt/sha256.h>
#include <tinycrypt/constants.h>
#include <pm_config.h>
#include <nrfx.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(assets, LOG_LEVEL_INF);

#define SECTOR 4096u
#define MAX_ASSETS 120
#define HEAD 32u
#define DATA0 (2 * SECTOR)
#define AREA PM_G1_ASSETS_SIZE
#define DIR_MAGIC 0x53414B53u   /* "SKAS" */
#define FORMAT_MAGIC 0x534B4153u
#define WIPE_MAGIC 0x5057u
#define SPACE_24 0x1000000u
#define CHIP_BYTES (DT_PROP(DT_NODELABEL(mx25r64), size) / 8)
#define READ_CHUNK 0x8000u

BUILD_ASSERT(PM_G1_ASSETS_ADDRESS == 0x140000, "the store starts above the factory record");
BUILD_ASSERT(PM_G1_ASSETS_ADDRESS + PM_G1_ASSETS_SIZE <= SPACE_24, "the store lies in the 24-bit space");
BUILD_ASSERT(PM_G1_ASSETS_SIZE % 0x10000 == 0, "whole 64 KiB blocks");
BUILD_ASSERT(!DT_PROP(DT_NODELABEL(mx25r64), address_size_32), "24-bit addressing only");
BUILD_ASSERT(HEAD + MAX_ASSETS * 32 + 4 <= SECTOR, "a directory copy fits its sector");

/* a directory entry as on flash (little-endian) */
struct entry {
	uint8_t id[16];
	uint32_t off, size, crc;
	uint8_t type, flags;
	uint16_t rsv;
};
BUILD_ASSERT(sizeof(struct entry) == 32);

enum { J_PUT = 1, J_DELETE = 2, J_FORMAT = 3, J_CRC = 4 };
enum { S_IDLE, S_ERASING, S_WRITING, S_VERIFYING, S_DONE, S_FAILED };
enum { CP_ERASED = 1, CP_MID_PROGRAM = 2, CP_VERIFIED = 3, CP_MID_DIRECTORY = 4 };

static const struct flash_area *fa;
static const struct device *const chip = DEVICE_DT_GET(DT_NODELABEL(mx25r64));
static bool avail;
static struct entry dir[MAX_ASSETS];
static uint8_t checked[MAX_ASSETS];   /* the first load's crc: 0 not yet, 1 good, 2 bad */
static uint16_t count;
static uint32_t seq;
static int copy = -1;                 /* the directory sector in force, -1 none */
static K_MUTEX_DEFINE(dir_lock);      /* the RAM directory */
static K_MUTEX_DEFINE(io_lock);       /* one flash call of a job; a whole ASSET_LOAD */
static uint8_t buf[SECTOR] __aligned(4);
static struct tc_sha256_state_struct sha;

static K_SEM_DEFINE(job_sem, 0, 1);
static atomic_t running, crc_pending, step_mode, paused, step_go, step_abort, reset_at;
static struct {
	uint8_t kind, type;
	bool wipe;
	uint8_t id[16];
	uint32_t addr, size, off;
} job;
static struct {
	uint32_t addr, n;
} crc_req;
static volatile uint8_t job_state, last_error;
static struct {
	uint32_t erase_us, program_us, verify_us, dir_us, longest_erase_us;
	uint32_t erased, commits, loads, load_bytes, check_us, steps, wiped;
} st;

static int find(const uint8_t *id)
{
	for (int i = 0; i < count; i++) {
		if (memcmp(dir[i].id, id, 16) == 0) {
			return i;
		}
	}
	return -1;
}

static uint32_t extent_end(const struct entry *e)
{
	return e->off + ROUND_UP(e->size, SECTOR);
}

/* a directory copy: magic, format 1, count, this build's partition size, extents in the data area and disjoint,
 * crc */
static bool valid(const uint8_t *b, uint16_t *n, uint32_t *s)
{
	const struct entry *e = (const struct entry *)(b + HEAD);
	uint16_t c = get16(b + 6);

	if (get32(b) != DIR_MAGIC || get16(b + 4) != 1 || c > MAX_ASSETS || get32(b + 12) != AREA ||
	    crc32_ieee(b, HEAD + 32u * c) != get32(b + HEAD + 32u * c)) {
		return false;
	}
	for (int i = 0; i < c; i++) {
		if (e[i].off % SECTOR || e[i].off < DATA0 || e[i].off >= AREA || e[i].size == 0 ||
		    e[i].size > ARENA_SIZE || extent_end(&e[i]) > AREA) {
			return false;
		}
		for (int j = 0; j < i; j++) {
			if (e[i].off < extent_end(&e[j]) && e[j].off < extent_end(&e[i])) {
				return false;
			}
		}
	}
	*n = c;
	*s = get32(b + 8);
	return true;
}

/* the RAM directory from a verified copy; entries kept keep their first-load check, a new one is good */
static void install(const uint8_t *b, uint16_t n, uint32_t s, const uint8_t *fresh)
{
	const struct entry *e = (const struct entry *)(b + HEAD);
	uint8_t c[MAX_ASSETS];

	k_mutex_lock(&dir_lock, K_FOREVER);
	for (int i = 0; i < n; i++) {
		int k = find(e[i].id);

		c[i] = fresh && memcmp(e[i].id, fresh, 16) == 0 ? 1 : k >= 0 ? checked[k] : 0;
	}
	memcpy(dir, e, 32u * n);
	memcpy(checked, c, n);
	count = n;
	seq = s;
	k_mutex_unlock(&dir_lock);
}

static int64_t alloc(uint32_t size)
{
	uint32_t need = ROUND_UP(size, SECTOR), at = DATA0;
	bool moved = true;

	while (moved) {
		moved = false;
		for (int i = 0; i < count; i++) {
			if (at < extent_end(&dir[i]) && dir[i].off < at + need) {
				at = extent_end(&dir[i]);
				moved = true;
			}
		}
	}
	return at + need <= AREA ? (int64_t)at : -1;
}

/* free bytes and the largest free extent of the data area (dir_lock held) */
static void space(uint32_t *free, uint32_t *largest, uint32_t *data)
{
	uint32_t at = DATA0, used = 0, big = 0, bytes = 0;

	for (;;) {
		int next = -1;

		for (int i = 0; i < count; i++) {
			if (dir[i].off >= at && (next < 0 || dir[i].off < dir[next].off)) {
				next = i;
			}
		}
		uint32_t end = next < 0 ? AREA : dir[next].off;

		big = MAX(big, end - at);
		if (next < 0) {
			break;
		}
		used += extent_end(&dir[next]) - dir[next].off;
		bytes += dir[next].size;
		at = extent_end(&dir[next]);
	}
	*free = AREA - DATA0 - used;
	*largest = big;
	*data = bytes;
}

/* ---------------------------------------------------------------- the store thread */

static void crc_job(void);

static int64_t us_now(void)
{
	return k_ticks_to_us_floor64(k_uptime_ticks());
}

/* DIAG SET 10: after every flash call that changes flash, wait for the host's step (a FLASH_CRC runs meanwhile);
 * false: the host aborted the job */
static bool step_point(void)
{
	st.steps++;
	if (!atomic_get(&step_mode)) {
		return true;
	}
	atomic_set(&paused, 1);
	for (;;) {
		k_sem_take(&job_sem, K_FOREVER);
		if (atomic_get(&crc_pending)) {
			crc_job();
		}
		if (atomic_cas(&step_abort, 1, 0)) {
			atomic_set(&paused, 0);
			return false;
		}
		if (atomic_cas(&step_go, 1, 0) || !atomic_get(&step_mode)) {
			break;
		}
	}
	atomic_set(&paused, 0);
	return true;
}

/* DIAG SET 9: a reset at checkpoint k of the next put, as a power failure would (test builds); not counted as an
 * unhealthy boot, since a power failure clears the retained counter too */
static void power_fail(const char *where)
{
	LOG_WRN("asset store: reset %s", where);
	LOG_PANIC();
	retained_mark_clean(CLEAN_HOST, false, false);
	sys_reboot(SYS_REBOOT_COLD);
}

static void checkpoint(int k)
{
	if (atomic_cas(&reset_at, k, 0)) {
		power_fail(k == CP_ERASED ? "after the erase" : k == CP_MID_PROGRAM ? "in the program"
							   : "after the verification");
	}
}

static int erase(uint32_t off)
{
	int64_t t;
	uint32_t us;
	int err;

	k_mutex_lock(&io_lock, K_FOREVER);
	t = us_now();
	err = flash_area_erase(fa, off, SECTOR);
	us = (uint32_t)(us_now() - t);
	k_mutex_unlock(&io_lock);
	st.erased++;
	st.longest_erase_us = MAX(st.longest_erase_us, us);
	return err;
}

/* the next directory copy (seq + 1, the other sector) with `add` and without `del`: erase, program, read back */
static int commit(const struct entry *add, const uint8_t *del)
{
	int target = copy == 0 ? 1 : 0, err;
	uint32_t next = seq + 1, len;
	uint16_t n = 0;
	uint32_t s;

	if (erase(target * SECTOR) || !step_point()) {
		return E_ASSET;
	}
	k_mutex_lock(&io_lock, K_FOREVER);
	k_mutex_lock(&dir_lock, K_FOREVER);
	memset(buf, 0xFF, SECTOR);
	memset(buf, 0, HEAD);
	put32(buf, DIR_MAGIC);
	put16(buf + 4, 1);
	put32(buf + 8, next);
	put32(buf + 12, AREA);
	for (int i = 0; i < count; i++) {
		if (!del || memcmp(dir[i].id, del, 16) != 0) {
			memcpy(buf + HEAD + 32u * n++, &dir[i], 32);
		}
	}
	k_mutex_unlock(&dir_lock);
	if (add) {
		memcpy(buf + HEAD + 32u * n++, add, 32);
	}
	put16(buf + 6, n);
	len = HEAD + 32u * n;
	put32(buf + len, crc32_ieee(buf, len));
	if (atomic_cas(&reset_at, CP_MID_DIRECTORY, 0)) {
		flash_area_write(fa, target * SECTOR, buf, HEAD);
		power_fail("in the directory write");
	}
	err = flash_area_write(fa, target * SECTOR, buf, SECTOR);
	k_mutex_unlock(&io_lock);
	if (err || !step_point()) {
		return E_ASSET;
	}
	k_mutex_lock(&io_lock, K_FOREVER);
	err = flash_area_read(fa, target * SECTOR, buf, SECTOR);
	if (!err && valid(buf, &n, &s) && s == next) {
		install(buf, n, s, add ? add->id : NULL);
		copy = target;
		st.commits++;
	} else {
		err = -EIO;
	}
	k_mutex_unlock(&io_lock);
	return err ? E_ASSET : 0;
}

static int put(void)
{
	uint32_t sectors = DIV_ROUND_UP(job.size, SECTOR), crc = 0;
	uint8_t digest[TC_SHA256_DIGEST_SIZE];
	struct entry e = {.off = job.off, .size = job.size, .type = job.type};
	int64_t t = us_now();

	for (uint32_t i = 0; i < sectors; i++) {
		if (erase(job.off + i * SECTOR) || !step_point()) {
			return E_ASSET;
		}
	}
	st.erase_us = (uint32_t)(us_now() - t);
	checkpoint(CP_ERASED);
	job_state = S_WRITING;
	t = us_now();
	for (uint32_t i = 0; i < sectors; i++) {
		uint32_t m = MIN(SECTOR, job.size - i * SECTOR);
		int err;

		k_mutex_lock(&io_lock, K_FOREVER);
		exec_arena_copy(job.addr + i * SECTOR, buf, m);
		memset(buf + m, 0xFF, SECTOR - m);
		err = flash_area_write(fa, job.off + i * SECTOR, buf, SECTOR);
		k_mutex_unlock(&io_lock);
		if (err || !step_point()) {
			return E_ASSET;
		}
		if (i == sectors / 2) {
			checkpoint(CP_MID_PROGRAM);
		}
	}
	st.program_us = (uint32_t)(us_now() - t);
	job_state = S_VERIFYING;
	t = us_now();
	tc_sha256_init(&sha);
	for (uint32_t i = 0; i < sectors; i++) {
		uint32_t m = MIN(SECTOR, job.size - i * SECTOR);
		int err;

		k_mutex_lock(&io_lock, K_FOREVER);
		err = flash_area_read(fa, job.off + i * SECTOR, buf, SECTOR);
		tc_sha256_update(&sha, buf, m);
		crc = crc32_ieee_update(crc, buf, m);
		k_mutex_unlock(&io_lock);
		if (err) {
			return E_ASSET;
		}
	}
	tc_sha256_final(digest, &sha);
	st.verify_us = (uint32_t)(us_now() - t);
	if (memcmp(digest, job.id, 16) != 0) {
		LOG_WRN("asset store: put failed, the body read back is not the ID's");
		return E_ASSET;
	}
	checkpoint(CP_VERIFIED);
	memcpy(e.id, job.id, 16);
	e.crc = crc;
	t = us_now();
	if (commit(&e, NULL)) {
		return E_ASSET;
	}
	st.dir_us = (uint32_t)(us_now() - t);
	return 0;
}

static int format(void)
{
	for (int i = 0; i < 2; i++) {
		if (erase(i * SECTOR) || !step_point()) {
			return E_ASSET;
		}
	}
	k_mutex_lock(&dir_lock, K_FOREVER);
	count = 0;
	seq = 0;
	k_mutex_unlock(&dir_lock);
	copy = -1;
	st.commits++;
	if (!job.wipe) {
		return 0;
	}
	st.wiped = 0;
	for (uint32_t off = DATA0; off < AREA; off += SECTOR) {
		bool erased = true;
		int err;

		k_mutex_lock(&io_lock, K_FOREVER);
		err = flash_area_read(fa, off, buf, SECTOR);
		for (uint32_t i = 0; i < SECTOR / 4 && erased; i++) {
			erased = ((const uint32_t *)buf)[i] == 0xFFFFFFFFu;
		}
		k_mutex_unlock(&io_lock);
		if (err) {
			return E_ASSET;
		}
		if (!erased) {
			if (erase(off) || !step_point()) {
				return E_ASSET;
			}
			st.wiped++;
		}
	}
	return 0;
}

static void notice(uint8_t kind, uint8_t result, const uint8_t *body16)
{
	uint8_t b[18];

	b[0] = kind;
	b[1] = result;
	memcpy(b + 2, body16, 16);
	proto_notice(N_ASSET, b, sizeof(b));
}

/* FLASH_CRC: crc32 and the bytes that are not 0xFF of a range of the 24-bit space, read only */
static void crc_job(void)
{
	uint32_t crc = 0, set = 0, a = crc_req.addr, end = crc_req.addr + crc_req.n;
	uint8_t b[16];
	int err = 0;

	while (a < end && !err) {
		uint32_t m = MIN(SECTOR, end - a);

		k_mutex_lock(&io_lock, K_FOREVER);
		err = flash_read(chip, a, buf, m);
		crc = crc32_ieee_update(crc, buf, m);
		for (uint32_t i = 0; i < m; i++) {
			set += buf[i] != 0xFF;
		}
		k_mutex_unlock(&io_lock);
		a += m;
	}
	put32(b, crc_req.addr);
	put32(b + 4, crc_req.n);
	put32(b + 8, crc);
	put32(b + 12, set);
	atomic_set(&crc_pending, 0);
	if (!atomic_get(&running)) {
		job_state = err ? S_FAILED : S_DONE;
		last_error = err ? E_ASSET : 0;
	}
	notice(J_CRC, err ? E_ASSET : 0, b);
}

static void run_job(void)
{
	static const uint8_t zero[16];
	int err;

	st.steps = 0;
	if (job.kind == J_PUT) {
		err = put();
	} else if (job.kind == J_DELETE) {
		int64_t t = us_now();

		err = commit(NULL, job.id);
		st.dir_us = (uint32_t)(us_now() - t);
	} else {
		err = format();
	}
	LOG_INF("asset store: job %u %s, %u assets, seq %u", job.kind, err ? "failed" : "done", count, seq);
	last_error = err;
	job_state = err ? S_FAILED : S_DONE;
	notice(job.kind, err, job.kind == J_FORMAT ? zero : job.id);
	job.kind = 0;
	atomic_set(&running, 0);
}

/* both directory copies at boot: the valid one with the higher seq, else an empty store */
static void load(void)
{
	uint16_t n;
	uint32_t s;

	for (int c = 0; c < 2; c++) {
		k_mutex_lock(&io_lock, K_FOREVER);
		if (flash_area_read(fa, c * SECTOR, buf, SECTOR) == 0 && valid(buf, &n, &s) && (copy < 0 || s > seq)) {
			install(buf, n, s, NULL);
			copy = c;
		}
		k_mutex_unlock(&io_lock);
	}
}

static void assets_thread(void *p1, void *p2, void *p3)
{
	bool addr24 = (NRF_QSPI->IFCONFIG0 & QSPI_IFCONFIG0_ADDRMODE_Msk) ==
		      (QSPI_IFCONFIG0_ADDRMODE_24BIT << QSPI_IFCONFIG0_ADDRMODE_Pos);

	if (!addr24 || !device_is_ready(chip) || flash_area_open(PM_G1_ASSETS_ID, &fa) != 0) {
		LOG_ERR("asset store unavailable: QSPI %s, flash %s", addr24 ? "24-bit" : "not 24-bit",
			device_is_ready(chip) ? "ready" : "not ready");
		avail = false;
	} else {
		load();
		avail = true;
		LOG_INF("asset store: %u assets, seq %u, copy %d", count, seq, copy);
	}
	for (;;) {
		k_sem_take(&job_sem, K_FOREVER);
		if (atomic_get(&crc_pending)) {
			crc_job();
		}
		if (job.kind) {
			run_job();
		}
	}
}

K_THREAD_DEFINE(assets_tid, 2048, assets_thread, NULL, NULL, NULL, 11, 0, 0);

/* ---------------------------------------------------------------- control requests (the evt thread) */

void assets_info(uint8_t *out)
{
	uint32_t free = 0, largest = 0, data = 0;

	k_mutex_lock(&dir_lock, K_FOREVER);
	if (avail) {
		space(&free, &largest, &data);
	}
	put32(out, data);
	put32(out + 4, free);
	put32(out + 8, largest);
	out[12] = count;
	out[13] = job_state;
	out[14] = last_error;
	out[15] = avail ? 1 : 0;
	put32(out + 16, seq);
	k_mutex_unlock(&dir_lock);
}

size_t assets_list(uint8_t first, uint8_t *out, size_t room)
{
	size_t n = 2;

	k_mutex_lock(&dir_lock, K_FOREVER);
	out[0] = count;
	out[1] = first;
	for (int i = first; i < count && n + 32 <= room && i < first + 7; i++) {
		memcpy(out + n, &dir[i], 32);
		n += 32;
	}
	k_mutex_unlock(&dir_lock);
	return n;
}

static int start(uint8_t kind, uint8_t state)
{
	if (!atomic_cas(&running, 0, 1)) {
		return E_ASSET;
	}
	job.kind = kind;
	job_state = state;
	k_sem_give(&job_sem);
	return 0;
}

int assets_put(const uint8_t *id, uint32_t addr, uint32_t size, uint8_t type, uint8_t *result)
{
	int64_t off;
	int k;

	if (safe_level()) {
		return E_SAFE_MODE;
	}
	if (size == 0 || size > ARENA_SIZE) {
		return E_ARG;
	}
	if ((uint64_t)addr + size > ARENA_SIZE) {
		return E_ARENA;
	}
	if (!avail) {
		return E_ASSET;
	}
	k_mutex_lock(&dir_lock, K_FOREVER);
	k = find(id);
	if (k >= 0) {
		uint32_t stored = dir[k].size;

		k_mutex_unlock(&dir_lock);
		*result = 1;
		return stored == size ? 0 : E_ARG;
	}
	off = count < MAX_ASSETS && !atomic_get(&running) ? alloc(size) : -1;
	k_mutex_unlock(&dir_lock);
	if (off < 0 || atomic_get(&running)) {
		return E_ASSET;
	}
	memcpy(job.id, id, 16);
	job.addr = addr;
	job.size = size;
	job.type = type;
	job.off = (uint32_t)off;
	*result = 0;
	return start(J_PUT, S_ERASING);
}

int assets_delete(const uint8_t *id)
{
	int k;

	if (safe_level()) {
		return E_SAFE_MODE;
	}
	if (!avail || atomic_get(&running)) {
		return E_ASSET;
	}
	k_mutex_lock(&dir_lock, K_FOREVER);
	k = find(id);
	k_mutex_unlock(&dir_lock);
	if (k < 0) {
		return E_ASSET;
	}
	memcpy(job.id, id, 16);
	return start(J_DELETE, S_VERIFYING);
}

int assets_format(const uint8_t *a, size_t alen)
{
	if (safe_level()) {
		return E_SAFE_MODE;
	}
	if (alen < 4 || get32(a) != FORMAT_MAGIC) {
		return E_ARG;
	}
	if (!avail || atomic_get(&running)) {
		return E_ASSET;
	}
	job.wipe = alen >= 8 && get32(a + 4) >> 16 == WIPE_MAGIC;
	return start(J_FORMAT, S_ERASING);
}

int assets_crc(uint32_t addr, uint32_t n)
{
	if (safe_level()) {
		return E_SAFE_MODE;
	}
	if (n == 0 || (uint64_t)addr + n > MIN(SPACE_24, CHIP_BYTES)) {
		return E_ARG;
	}
	if (!device_is_ready(chip) || atomic_get(&crc_pending) || (atomic_get(&running) && !atomic_get(&paused))) {
		return E_ASSET;
	}
	crc_req.addr = addr;
	crc_req.n = n;
	atomic_set(&crc_pending, 1);
	if (!atomic_get(&running)) {
		job_state = S_VERIFYING;
	}
	k_sem_give(&job_sem);
	return 0;
}

/* ---------------------------------------------------------------- ASSET_LOAD (the executor) */

int assets_read(const uint8_t *id, uint32_t off, uint8_t *dst, uint32_t n)
{
	struct entry e = {0};
	uint32_t gen = 0;
	int k, check = 0, err = 0;

	if (!avail) {
		return VM_E_ASSET;
	}
	k_mutex_lock(&io_lock, K_FOREVER);
	k_mutex_lock(&dir_lock, K_FOREVER);
	k = find(id);
	if (k >= 0) {
		e = dir[k];
		check = checked[k];
		gen = seq;
	}
	k_mutex_unlock(&dir_lock);
	if (k < 0 || ((uint64_t)off + n <= e.size && check == 2)) {
		k_mutex_unlock(&io_lock);
		return VM_E_ASSET;
	}
	if ((uint64_t)off + n > e.size) {
		k_mutex_unlock(&io_lock);
		return VM_E_ARENA;
	}
	if (check == 0) {
		int64_t t = us_now();
		uint32_t crc = 0;

		for (uint32_t a = 0; a < e.size && !err; a += SECTOR) {
			uint32_t m = MIN(SECTOR, e.size - a);

			err = flash_area_read(fa, e.off + a, buf, m);
			crc = crc32_ieee_update(crc, buf, m);
		}
		check = !err && crc == e.crc ? 1 : 2;
		st.check_us = (uint32_t)(us_now() - t);
		k_mutex_lock(&dir_lock, K_FOREVER);
		if (seq == gen && memcmp(dir[k].id, id, 16) == 0) {
			checked[k] = check;
		}
		k_mutex_unlock(&dir_lock);
		if (check == 2) {
			LOG_WRN("asset store: an asset fails its crc, refused until deleted");
			k_mutex_unlock(&io_lock);
			return VM_E_ASSET;
		}
	}
	for (uint32_t done = 0; done < n && !err; done += READ_CHUNK) {
		err = flash_area_read(fa, e.off + off + done, dst + done, MIN(READ_CHUNK, n - done));
	}
	k_mutex_unlock(&io_lock);
	st.loads++;
	st.load_bytes += n;
	return err ? VM_E_ASSET : 0;
}

/* ---------------------------------------------------------------- diagnostics (test builds) */

void assets_set(uint8_t item, uint8_t value)
{
	if (item == 9) {
		atomic_set(&reset_at, value);
	} else if (value == 2) {
		atomic_set(&step_go, 1);
	} else if (value == 3) {
		atomic_set(&step_abort, 1);
	} else {
		atomic_set(&step_mode, value);
	}
	k_sem_give(&job_sem);
}

size_t assets_diag(uint8_t *out)
{
	size_t unused = SIZE_MAX;

#if defined(CONFIG_INIT_STACKS)
	k_thread_stack_space_get(assets_tid, &unused);
#endif
	put32(out, st.erase_us);
	put32(out + 4, st.program_us);
	put32(out + 8, st.verify_us);
	put32(out + 12, st.dir_us);
	put32(out + 16, st.longest_erase_us);
	put32(out + 20, st.erased);
	put32(out + 24, st.commits);
	put32(out + 28, st.loads);
	put32(out + 32, st.load_bytes);
	put32(out + 36, st.check_us);
	put32(out + 40, st.steps);
	put32(out + 44, st.wiped);
	out[48] = atomic_get(&paused);
	out[49] = atomic_get(&step_mode);
	out[50] = atomic_get(&reset_at);
	out[51] = copy < 0 ? 0xFF : copy;
	put32(out + 52, (uint32_t)unused);
	return 56;
}
