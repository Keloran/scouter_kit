/* DIAG 19 BONDS (read only): the bonds Zephyr holds, with the aging counter that decides which one a new
 * pairing replaces when all CONFIG_BT_MAX_PAIRED are used (docs/fps-link.md §13.1). The counter and the
 * key flags live in the host's internal struct bt_keys (subsys/bluetooth/host/keys.h, NCS 2.5.1); nothing
 * here writes to it. Reply: u8 count, u32 the highest of their counters, then per bond 16 bytes: u8
 * address type, 6 address bytes (LSB first), u16 key types, u8 flags, u8 key size, u32 aging counter,
 * u8 1 when the bond's peer is connected now. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/sys/util.h>
#include <zephyr/bluetooth/addr.h>
#include <zephyr/bluetooth/conn.h>
#include "keys.h"
#include "g1.h"

#define BOND_LEN 16

struct walk {
	uint8_t *out;
	size_t max;
	uint8_t count;
	uint32_t newest;
};

struct peer {
	const bt_addr_le_t *addr;
	bool found;
};

static void match_conn(struct bt_conn *c, void *data)
{
	struct peer *p = data;

	if (bt_addr_le_eq(bt_conn_get_dst(c), p->addr)) {
		p->found = true;
	}
}

static void one(struct bt_keys *k, void *data)
{
	struct walk *w = data;
	struct peer p = {.addr = &k->addr};
	uint8_t *e = w->out + 5 + BOND_LEN * w->count;

	if (k->id != BT_ID_DEFAULT || 5 + BOND_LEN * (w->count + 1) > w->max) {
		return;
	}
	bt_conn_foreach(BT_CONN_TYPE_LE, match_conn, &p);
	e[0] = k->addr.type;
	memcpy(&e[1], k->addr.a.val, 6);
	put16(&e[7], k->keys);
	e[9] = k->flags;
	e[10] = k->enc_size;
	put32(&e[11], k->aging_counter);
	e[15] = p.found;
	w->newest = MAX(w->newest, k->aging_counter);
	w->count++;
}

size_t bonds_diag(uint8_t *out, size_t max)
{
	struct walk w = {.out = out, .max = max};

	if (max < 5) {
		return 0;
	}
	bt_keys_foreach_type(BT_KEYS_ALL, one, &w);
	out[0] = w.count;
	put32(&out[1], w.newest);
	return 5 + BOND_LEN * w.count;
}
