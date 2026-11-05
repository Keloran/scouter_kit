/* The arm-to-arm message link (VM.md §6.9) over the stock network core's ESB (netcore.c, review-v5.md §8.7). The net
 * core holds one 32-byte frame at a time: the right arm's (the master) sends it instead of its next announcement,
 * every 50 ms, and the left arm's returns it in its acknowledgements; it reports when the frame went out, so the
 * next frame waits for that report. While PEER_LINK is 1 the right arm sends a frame every 100 ms and at once when
 * there is news (PEER_SEND, its BRIGHTNESS); the left sends one at once for its PEER_SEND and otherwise every
 * 200 ms, so each slot is mostly free for news. A frame carries the sender's message
 * until the other arm acknowledges it, the right arm's brightness (for a left arm with AUTO_BRIGHTNESS = 1) and a
 * clock reference: the right arm's delivery report and the left arm's arrival of the same frame mark the same
 * exchange, so the left learns the offset between the clocks and returns it, and PEER_T_US is the sender's
 * PEER_SEND time in the receiver's clock. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/logging/log.h>
#include <zephyr/random/rand32.h>
#include "g1.h"
#include "vm.h"

LOG_MODULE_REGISTER(peer, LOG_LEVEL_INF);

#define MARK 0x47
#define F_MSG BIT(0)
#define F_LEVEL BIT(1)
#define F_REF BIT(2)
#define F_ACK BIT(3)
#define F_OFFSET BIT(4)
#define POLL_MS 100
#define KEEPALIVE_MS 200
#define HEARD_MS 500
#define REPORT_TIMEOUT_MS 300   /* a lost report: the net core refuses a frame while its slot is full, so retrying is safe */
#define INBOX 4
#define SAMPLES 9

static K_SEM_DEFINE(sem, 0, 1);
static struct k_spinlock lock;
static bool available, master;
static atomic_t on, follow, kick;
static uint8_t nonce;

/* the message this arm sends (one slot) and the last one it received */
static uint8_t tx_seq;
static uint32_t tx_value, tx_t;
static bool tx_pending;
static uint8_t rx_seq, rx_nonce, rx_id;
static bool rx_any, heard_any;
static uint32_t msg = VM_UNKNOWN, msg_t = VM_UNKNOWN;
static int64_t heard_ms;

/* the frame in the net core's slot */
static bool inflight;
static uint8_t frame_id, inflight_id, sent_level = 0xFF;
static int64_t queued_ms;
static uint32_t queued_us;

/* the clock reference: the right arm's delivery report of frame ref_id; the left's arrival times */
static uint8_t ref_id;
static uint32_t ref_t;
static bool ref_ok;
static struct {
	uint8_t id;
	bool ok;
	uint32_t t;
} arrival[16];
static int32_t samples[SAMPLES], offset;
static int nsamples, next_sample;
static bool offset_ok;

/* from the IPC callback */
static uint8_t inbox[INBOX][32];
static uint32_t inbox_t[INBOX];
static uint8_t in_head, in_tail;
static uint32_t sent_t;
static atomic_t sent_flag;

static struct peer_stats st;

void peer_start(bool is_master)
{
	master = is_master;
	nonce = (sys_rand32_get() % 255) + 1;
	available = true;
	LOG_INF("arm-to-arm link available (%s)", master ? "ESB master" : "ESB slave");
}

bool peer_available(void)
{
	return available;
}

/* IPC callback (netcore.c): a frame from the other arm, the delivery of ours, the net core's link report */
void peer_net_frame(const uint8_t *f)
{
	k_spinlock_key_t key = k_spin_lock(&lock);
	uint8_t next = (in_head + 1) % INBOX;

	if (f[0] != MARK) {
		k_spin_unlock(&lock, key);
		return;
	}
	if (next == in_tail) {
		st.inbox_drops++;
	} else {
		memcpy(inbox[in_head], f, 32);
		inbox_t[in_head] = now_us();
		in_head = next;
	}
	k_spin_unlock(&lock, key);
	k_sem_give(&sem);
}

void peer_net_sent(uint8_t v)
{
	sent_t = now_us();
	atomic_set(&sent_flag, 1);
	k_sem_give(&sem);
}

void peer_net_report(uint32_t acked, uint32_t failed, uint32_t received, uint8_t value)
{
	st.reports++;
	st.net_acked = acked;
	st.net_failed = failed;
	st.net_received = received;
	st.link_value = value;
	st.report_t = now_us();
	touch_link_value(value);
}

static int32_t median(void)
{
	int32_t s[SAMPLES];
	int n = nsamples;

	memcpy(s, samples, sizeof(s));
	for (int i = 1; i < n; i++) {
		for (int j = i; j > 0 && s[j - 1] > s[j]; j--) {
			int32_t x = s[j];

			s[j] = s[j - 1];
			s[j - 1] = x;
		}
	}
	return s[n / 2];
}

/* the receiver's clock for a time of the sender's, else when the frame arrived */
static uint32_t local_time(uint32_t t, uint32_t arrived)
{
	if (!offset_ok) {
		return arrived;
	}
	return master ? t - (uint32_t)offset : t + (uint32_t)offset;
}

static void receive(const uint8_t *p, uint32_t t)
{
	uint8_t flags = p[1];
	bool deliver = false;

	if (heard_any && p[17] == rx_id && p[19] == rx_nonce) {
		return;   /* the same frame again (the net core may send an acknowledgement payload twice) */
	}
	st.frames_in++;
	heard_any = true;
	rx_id = p[17];
	heard_ms = k_uptime_get();
	if (p[19] != rx_nonce) {
		rx_nonce = p[19];
		rx_any = false;
	}
	if (tx_pending && (flags & F_ACK) && p[24] == nonce && p[3] == tx_seq) {
		tx_pending = false;
	}
	if ((flags & F_MSG) && (!rx_any || p[2] != rx_seq)) {
		rx_seq = p[2];
		rx_any = true;
		deliver = true;
	}
	if (master) {
		if (flags & F_OFFSET) {
			offset = (int32_t)get32(&p[20]);
			offset_ok = true;
		}
	} else {
		arrival[p[17] % 16].id = p[17];
		arrival[p[17] % 16].ok = true;
		arrival[p[17] % 16].t = t;
		if ((flags & F_REF) && arrival[p[16] % 16].ok && arrival[p[16] % 16].id == p[16]) {
			samples[next_sample] = (int32_t)(arrival[p[16] % 16].t - get32(&p[12]));
			next_sample = (next_sample + 1) % SAMPLES;
			nsamples = MIN(nsamples + 1, SAMPLES);
			offset = median();
			offset_ok = true;
		}
		if ((flags & F_LEVEL) && atomic_get(&follow) && p[18] <= PANEL_LEVEL_MAX && p[18] != panel_level()) {
			uint8_t out[PANEL_LEVEL_LEN];

			panel_set_level(p[18], out);
			st.levels_applied++;
		}
	}
	if (deliver) {
		k_spinlock_key_t key = k_spin_lock(&lock);

		msg = get32(&p[4]);
		msg_t = local_time(get32(&p[8]), t);
		k_spin_unlock(&lock, key);
		st.delivered++;
		st.last_arrival_t = t;
		exec_trigger(VM_T_PEER, msg);
	}
}

static void build(uint8_t *m)
{
	uint8_t *p = &m[1];
	k_spinlock_key_t key = k_spin_lock(&lock);

	memset(m, 0, 33);
	m[0] = master ? 0x02 : 0x04;
	p[0] = MARK;
	p[1] = (tx_pending ? F_MSG : 0) | (rx_any ? F_ACK : 0);
	p[2] = tx_seq;
	p[3] = rx_seq;
	put32(&p[4], tx_value);
	put32(&p[8], tx_t);
	k_spin_unlock(&lock, key);
	if (master) {
		p[1] |= F_LEVEL | (ref_ok ? F_REF : 0);
		put32(&p[12], ref_t);
		p[16] = ref_id;
		p[18] = panel_level();
	} else if (offset_ok) {
		p[1] |= F_OFFSET;
		put32(&p[20], (uint32_t)offset);
	}
	p[17] = ++frame_id;
	p[19] = nonce;
	p[24] = rx_nonce;
}

static void send(int64_t now)
{
	uint8_t m[33];
	bool news = atomic_get(&kick) || (master && panel_level() != sent_level);

	if (!atomic_get(&on)) {
		return;
	}
	if (inflight && now - queued_ms < REPORT_TIMEOUT_MS) {
		return;
	}
	if (inflight) {
		inflight = false;
		st.report_timeouts++;
	}
	if (!news && now - queued_ms < (master ? POLL_MS : KEEPALIVE_MS)) {
		return;
	}
	atomic_set(&kick, 0);
	build(m);
	if (g1_netcore_send(m, sizeof(m)) < 0) {
		st.send_errors++;
		return;
	}
	inflight = true;
	inflight_id = frame_id;
	queued_ms = now;
	queued_us = now_us();
	sent_level = m[19];
	st.frames_out++;
}

static void peer_thread(void *a, void *b, void *c)
{
	for (;;) {
		int64_t now = k_uptime_get();
		k_timeout_t wait = K_FOREVER;

		if (atomic_get(&on)) {
			int64_t due = queued_ms + (inflight ? REPORT_TIMEOUT_MS : master ? POLL_MS : KEEPALIVE_MS);

			wait = K_MSEC(MAX(due - now, 1));
		}
		k_sem_take(&sem, wait);
		if (atomic_set(&sent_flag, 0)) {
			if (inflight) {
				uint32_t d = sent_t - queued_us;

				inflight = false;
				st.reports_sent++;
				st.delay_last_us = d;
				st.delay_max_us = MAX(st.delay_max_us, d);
				st.delay_total_us += d;
				if (master) {
					ref_id = inflight_id;
					ref_t = sent_t;
					ref_ok = true;
				}
			}
		}
		for (;;) {
			k_spinlock_key_t key = k_spin_lock(&lock);
			uint8_t f[32];
			uint32_t t;

			if (in_tail == in_head) {
				k_spin_unlock(&lock, key);
				break;
			}
			memcpy(f, inbox[in_tail], 32);
			t = inbox_t[in_tail];
			in_tail = (in_tail + 1) % INBOX;
			k_spin_unlock(&lock, key);
			if (atomic_get(&on)) {
				receive(f, t);
			}
		}
		send(k_uptime_get());
	}
}

K_THREAD_DEFINE(peer_tid, 1024, peer_thread, NULL, NULL, NULL, 7, 0, 0);

/* VM properties (exec.c) */
uint32_t peer_link(void)
{
	if (!atomic_get(&on)) {
		return 0;
	}
	return heard_any && k_uptime_get() - heard_ms < HEARD_MS ? 2 : 1;
}

int peer_set_link(uint32_t v)
{
	if (!available || v > 1) {
		return VM_E_ARG;
	}
	if (atomic_set(&on, v) != (atomic_val_t)v) {
		LOG_INF("PEER_LINK %u", v);
	}
	if (!v) {
		atomic_set(&follow, 0);
		tx_pending = false;
	} else {
		atomic_set(&kick, 1);
	}
	k_sem_give(&sem);
	return 0;
}

int peer_send(uint32_t v)
{
	k_spinlock_key_t key;

	if (!atomic_get(&on)) {
		return VM_E_ARG;
	}
	key = k_spin_lock(&lock);
	tx_seq++;
	tx_value = v;
	tx_t = now_us();
	tx_pending = true;
	k_spin_unlock(&lock, key);
	atomic_set(&kick, 1);
	k_sem_give(&sem);
	return 0;
}

uint32_t peer_msg(bool time)
{
	k_spinlock_key_t key = k_spin_lock(&lock);
	uint32_t v = time ? msg_t : msg;

	k_spin_unlock(&lock, key);
	return v;
}

/* AUTO_BRIGHTNESS on the left arm: follow the right arm's level, only while PEER_LINK is 1 */
int peer_set_follow(uint32_t v)
{
	if (v && (master || !atomic_get(&on))) {
		return VM_E_ARG;
	}
	atomic_set(&follow, v);
	return 0;
}

bool peer_follow(void)
{
	return atomic_get(&follow);
}

/* the right arm's brightness changed */
void peer_kick(void)
{
	if (master && atomic_get(&on)) {
		k_sem_give(&sem);
	}
}

void peer_reset(void)
{
	peer_set_link(0);
}

void peer_get_stats(struct peer_stats *s)
{
	*s = st;
	s->available = available;
	s->link = peer_link();
	s->follow = peer_follow();
	s->offset = offset_ok ? offset : (int32_t)VM_UNKNOWN;
	s->other_msgs = netcore_other_msgs;
}
