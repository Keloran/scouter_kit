/* The arm mirror of g1host.py: the ScouterKit VM core (../src/vm) on a plain platform
 * that runs host packets only (VM.md §5.6: programs are not mirrored), with a virtual clock, fixed
 * property values, a 640 x 480 panel image and a list of the events the core made. g1host.py builds it
 * with the core into a shared library and calls it through ctypes. */
#include <stdlib.h>
#include <string.h>
#include "vm.h"

#define MAXEV 256
#define MAXASSETS 16

struct mirror {
	struct vm vm;
	struct vm_host host;
	uint8_t arena[VM_ARENA_SIZE];
	uint8_t panel[VM_PANEL_H * VM_PANEL_W];
	uint32_t props[64], t;
	const uint8_t *pkt;
	uint32_t pkt_len;
	int panel_on, standby, has_panel;
	uint32_t triggers, peer_link;
	uint32_t brightness, nev, ev_len[MAXEV];
	uint8_t ev[MAXEV][32];
	struct {
		uint8_t id[16];
		uint8_t *data;
		uint32_t n;
	} asset[MAXASSETS];   /* the stored assets this host knows the bytes of (ASSET_LOAD, VM.md §6.10) */
};

static uint32_t m_now(void *c)
{
	return ((struct mirror *)c)->t;
}

static const uint8_t *m_packet(void *c, uint32_t *len)
{
	struct mirror *m = c;

	*len = m->pkt_len;
	return m->pkt;
}

static void m_packet_done(void *c)
{
	((struct mirror *)c)->pkt = NULL;
}

static uint32_t m_poll(void *c)
{
	return 0;
}

static uint32_t m_prop(void *c, uint32_t id)
{
	struct mirror *m = c;

	if (id == VM_P_PANEL) {
		return m->panel_on ? 1 : m->standby ? 2 : 0;
	}
	if (id == VM_P_BRIGHTNESS) {
		return m->brightness;
	}
	if (id == VM_P_PEER_LINK) {
		return VM_UNKNOWN;   /* 2 while the other arm answers: not known here */
	}
	return id < 64 ? m->props[id] : VM_UNKNOWN;
}

static int m_event(void *c, uint8_t type, const uint8_t *body, uint32_t len)
{
	struct mirror *m = c;

	if (m->nev < MAXEV && len < 32) {
		m->ev[m->nev][0] = type;
		memcpy(m->ev[m->nev] + 1, body, len);
		m->ev_len[m->nev++] = len + 1;
	}
	return 0;
}

static int m_panel_begin(void *c, int32_t x, int32_t y, int32_t w, int32_t h)
{
	struct mirror *m = c;

	if (!m->has_panel) {
		return 1;
	}
	if (!m->panel_on) {
		m->panel_on = 1;
		m->standby = 0;
		memset(m->panel, 0, sizeof(m->panel));
	}
	return 0;
}

static void m_panel_row(void *c, int32_t x, int32_t y, int32_t w, const uint8_t *px)
{
	uint8_t *p = ((struct mirror *)c)->panel + (size_t)y * VM_PANEL_W + x;

	for (int32_t i = 0; i < w; i++) {
		p[i] = (i & 1) ? (px[i / 2] & 15) : (px[i / 2] >> 4);
	}
}

static void m_panel_end(void *c)
{
}

static void m_panel_power(void *c, uint32_t on)
{
	struct mirror *m = c;

	if (on != 1 || !m->panel_on) {
		memset(m->panel, 0, sizeof(m->panel));
	}
	m->panel_on = on == 1 && m->has_panel;
	m->standby = on == 2 && m->has_panel;
}

static void m_brightness(void *c, uint32_t level)
{
	((struct mirror *)c)->brightness = level;
}

static void m_imu_rate(void *c, uint32_t hz)
{
}

/* The settings the arm owns, refused as the arm refuses them (its HELLO.triggers, mirror_set_triggers): GYRO_RATE
 * without the IMU; AUTO_BRIGHTNESS without the light sensor, except on a left arm with the arm-to-arm link while
 * PEER_LINK is 1 (it follows the right arm); PEER_LINK and PEER_SEND without the link, PEER_SEND while PEER_LINK
 * is 0. PEER_LINK = 0 ends the left arm's following. BRIGHTNESS_MAX is always taken (the core checked 0...42). */
static int refused(const struct mirror *m, uint32_t id, uint32_t value)
{
	int imu = m->triggers & (1u << VM_T_MOTION), light = m->triggers & (1u << VM_T_LIGHT);
	int peer = m->triggers & (1u << VM_T_PEER), left = m->props[VM_P_ARM] == 1;

	switch (id) {
	case VM_P_BRIGHTNESS_MAX:
		return 0;
	case VM_P_GYRO_RATE:
		return imu ? 0 : VM_E_ARG;
	case VM_P_AUTO_BRIGHTNESS:
		return light || (peer && left && (m->peer_link || !value)) ? 0 : VM_E_ARG;
	case VM_P_PEER_LINK:
		return peer && value <= 1 ? 0 : VM_E_ARG;
	case VM_P_PEER_SEND:
		return peer && m->peer_link ? 0 : VM_E_ARG;
	}
	return VM_E_ARG;
}

static int m_prop_write(void *c, uint32_t id, uint32_t value)
{
	struct mirror *m = c;
	int err = refused(m, id, value);

	if (err) {
		return err;
	}
	if (id == VM_P_PEER_LINK) {
		m->peer_link = value;
		if (!value && !(m->triggers & (1u << VM_T_LIGHT))) {
			m->props[VM_P_AUTO_BRIGHTNESS] = 0;
		}
	} else if (id != VM_P_PEER_SEND) {
		m->props[id] = value;
	}
	return 0;
}

/* 0 if the arm would take this SYS_WRITE of a setting it owns, else its error code (nothing changes) */
int mirror_check_write(struct mirror *m, uint32_t id, uint32_t value)
{
	if (id == VM_P_GYRO_RATE && value != 0 && value != 13 && value != 26 && value != 52 && value != 104) {
		return VM_E_ARG;
	}
	if (id == VM_P_AUTO_BRIGHTNESS && value > 1) {
		return VM_E_ARG;
	}
	return refused(m, id, value);
}

static void sensor_settings(struct mirror *m)
{
	m->props[VM_P_GYRO_RATE] = m->triggers & (1u << VM_T_MOTION) ? 0 : VM_UNKNOWN;
	m->props[VM_P_AUTO_BRIGHTNESS] = m->triggers & (1u << VM_T_LIGHT) ||
						 (m->triggers & (1u << VM_T_PEER) && m->props[VM_P_ARM] == 1)
					 ? 0
					 : VM_UNKNOWN;
	m->props[VM_P_BRIGHTNESS_MAX] = 42;
	m->peer_link = 0;
}

static int m_asset_read(void *c, const uint8_t *id, uint32_t off, uint8_t *dst, uint32_t n)
{
	struct mirror *m = c;

	for (int i = 0; i < MAXASSETS; i++) {
		if (m->asset[i].data && memcmp(m->asset[i].id, id, 16) == 0) {
			if ((uint64_t)off + n > m->asset[i].n) {
				return VM_E_ARENA;
			}
			memcpy(dst, m->asset[i].data + off, n);
			return 0;
		}
	}
	return VM_E_ASSET;
}

/* an asset whose bytes the host knows; n = 0 forgets it */
int mirror_set_asset(struct mirror *m, const uint8_t *id, const uint8_t *data, uint32_t n)
{
	int free_slot = -1;

	for (int i = 0; i < MAXASSETS; i++) {
		if (m->asset[i].data && memcmp(m->asset[i].id, id, 16) == 0) {
			free(m->asset[i].data);
			m->asset[i].data = NULL;
		}
		if (!m->asset[i].data && free_slot < 0) {
			free_slot = i;
		}
	}
	if (n == 0) {
		return 0;
	}
	if (free_slot < 0 || !(m->asset[free_slot].data = malloc(n))) {
		return -1;
	}
	memcpy(m->asset[free_slot].id, id, 16);
	memcpy(m->asset[free_slot].data, data, n);
	m->asset[free_slot].n = n;
	return 0;
}

/* HELLO.triggers: which of the arm's own settings it has (above) */
void mirror_set_triggers(struct mirror *m, uint32_t triggers)
{
	m->triggers = triggers;
	sensor_settings(m);
}

struct mirror *mirror_new(int arm, int has_panel)
{
	struct mirror *m = calloc(1, sizeof(*m));
	struct vm_host h = {m, m_now, m_packet, m_packet_done, m_poll, m_prop, m_event, m_panel_begin,
			    m_panel_row, m_panel_end, m_panel_power, m_brightness, m_imu_rate, m_prop_write,
			    NULL, m_asset_read};

	if (!m) {
		return NULL;
	}
	m->host = h;
	for (int i = 0; i < 64; i++) {
		m->props[i] = VM_UNKNOWN;
	}
	m->props[VM_P_ARM] = (uint32_t)arm;
	m->props[VM_P_LINK] = 1;
	m->triggers = 0xFFFF;
	sensor_settings(m);
	m->brightness = 21;
	m->has_panel = has_panel;
	vm_init(&m->vm, m->arena, &m->host, 0);
	return m;
}

void mirror_free(struct mirror *m)
{
	for (int i = 0; i < MAXASSETS; i++) {
		free(m->asset[i].data);
	}
	free(m);
}

uint8_t *mirror_arena(struct mirror *m)
{
	return m->arena;
}

uint8_t *mirror_panel(struct mirror *m)
{
	return m->panel;
}

int mirror_panel_on(struct mirror *m)
{
	return m->panel_on;
}

void mirror_set_panel(struct mirror *m, int on)
{
	m_panel_power(m, (uint32_t)on);
}

void mirror_set_time(struct mirror *m, uint32_t t)
{
	m->t = t;
}

uint32_t mirror_time(struct mirror *m)
{
	return m->t;
}

void mirror_set_prop(struct mirror *m, uint32_t id, uint32_t v)
{
	if (id < 64) {
		m->props[id] = v;
	}
}

/* Runs one packet (u16 seq + host code) to its end; SLEEP_UNTIL advances the virtual clock. Returns the
 * number of events it made (mirror_event reads them). */
int mirror_packet(struct mirror *m, const uint8_t *pkt, uint32_t len)
{
	m->nev = 0;
	m->pkt = pkt;
	m->pkt_len = len;
	for (int guard = 0; m->pkt && guard < 1000; guard++) {
		uint32_t w = vm_run(&m->vm);

		if (w == VM_FOREVER || w == 0) {
			break;
		}
		m->t += w;
	}
	m->pkt = NULL;
	return (int)m->nev;
}

/* event i of the last packet: type, then the body (from offset 9 of the event); returns its length */
int mirror_event(struct mirror *m, int i, uint8_t *out)
{
	if (i < 0 || (uint32_t)i >= m->nev) {
		return 0;
	}
	memcpy(out, m->ev[i], m->ev_len[i]);
	return (int)m->ev_len[i];
}

void mirror_reset(struct mirror *m, int clear_arena)
{
	vm_reset_session(&m->vm, clear_arena);
	sensor_settings(m);
}
