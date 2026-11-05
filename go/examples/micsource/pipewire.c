#include "pipewire.h"

#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <pipewire/pipewire.h>
#include <spa/param/audio/format-utils.h>
#include <spa/utils/ringbuffer.h>

#define RING_BYTES (1 << 15) /* 16384 samples */
#define MAX_CORRECTION 0.005

static pthread_once_t once = PTHREAD_ONCE_INIT;

static void init(void) { pw_init(NULL, NULL); }

static const struct spa_pod *format(struct spa_pod_builder *b, int rate) {
  return spa_format_audio_raw_build(
      b, SPA_PARAM_EnumFormat,
      &SPA_AUDIO_INFO_RAW_INIT(.format = SPA_AUDIO_FORMAT_S16, .channels = 1, .rate = (uint32_t)rate,
                               .position = {SPA_AUDIO_CHANNEL_MONO}));
}

static int wait_state(struct pw_thread_loop *loop, atomic_int *state, int timeout_ms) {
  struct timespec until;
  pw_thread_loop_get_time(loop, &until, (int64_t)timeout_ms * SPA_NSEC_PER_MSEC);
  int st;
  while ((st = atomic_load(state)) != PW_STREAM_STATE_PAUSED && st != PW_STREAM_STATE_STREAMING &&
         st != PW_STREAM_STATE_ERROR)
    if (pw_thread_loop_timed_wait_full(loop, &until) != 0) break;
  return atomic_load(state);
}

/* ------------------------------------------------------------ the source */

struct mic_source {
  struct pw_thread_loop *loop;
  struct pw_stream *stream;
  struct spa_ringbuffer ring;
  uint8_t data[RING_BYTES];
  int rate, target;
  int buffering, settle; /* the data thread's; settle: samples until the controller integrates again */
  double average, integral;
  char error[128]; /* under the loop lock */
  atomic_int state, adaptive, fill;
  _Atomic double correction;
  atomic_uint_fast64_t cycles, played, silence, underruns, overruns, skipped;
};

static void add(atomic_uint_fast64_t *counter, uint64_t n) {
  atomic_fetch_add_explicit(counter, n, memory_order_relaxed);
}

static void source_state(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
  struct mic_source *s = data;
  (void)old;
  atomic_store(&s->state, state);
  if (error) snprintf(s->error, sizeof(s->error), "%s", error);
  pw_thread_loop_signal(s->loop, false);
}

/* Keeps the average queue at the target by nudging the stream's resampler
   (the arm's clock and the graph's differ by up to a few hundred ppm); a
   rate above 1 takes fewer samples per cycle. */
static void adjust(struct mic_source *s, int32_t fill, uint32_t n) {
  double dt = (double)n / s->rate;
  s->average += (fill - s->average) * (dt < 4 ? dt / 4 : 1);
  if (s->settle > 0) {
    s->settle -= (int)n;
    return;
  }
  double error = (s->average - s->target) / s->rate;
  if (error > 0.03) error = 0.03;
  if (error < -0.03) error = -0.03;
  s->integral += error * dt;
  if (s->integral > 2.5) s->integral = 2.5;
  if (s->integral < -2.5) s->integral = -2.5;
  double c = 1 - 0.1 * error - 0.002 * s->integral;
  if (c > 1 + MAX_CORRECTION) c = 1 + MAX_CORRECTION;
  if (c < 1 - MAX_CORRECTION) c = 1 - MAX_CORRECTION;
  if (!atomic_load(&s->adaptive)) return;
  if (pw_stream_set_rate(s->stream, c) < 0)
    atomic_store(&s->adaptive, 0);
  else
    atomic_store(&s->correction, c);
}

/* Drops the oldest queued samples, down to the target and half an event
   (it runs when an event came in: the queue's mean stays on the target). */
static int32_t drop(struct mic_source *s, uint32_t *index, int32_t avail) {
  int32_t keep = s->target + s->rate / 40;
  if (avail <= keep) return avail;
  uint32_t skip = (uint32_t)(avail - keep);
  *index += skip * 2;
  spa_ringbuffer_read_update(&s->ring, (int32_t)*index);
  add(&s->skipped, skip);
  s->average = s->target;
  s->settle = s->rate;
  return keep;
}

static void source_process(void *data) {
  struct mic_source *s = data;
  struct pw_buffer *b = pw_stream_dequeue_buffer(s->stream);
  if (!b) return;
  struct spa_data *d = &b->buffer->datas[0];
  if (!d->data) {
    pw_stream_queue_buffer(s->stream, b);
    return;
  }
  int16_t *out = d->data;
  uint32_t n = d->maxsize / 2;
  if (b->requested && b->requested < n) n = (uint32_t)b->requested;
  uint32_t index;
  int32_t avail = spa_ringbuffer_get_read_index(&s->ring, &index) / 2;
  if (s->buffering) {
    if (avail >= s->target) {
      s->buffering = 0;
      s->average = avail;
      s->settle = s->rate;
      /* filled up (at the start, after an underrun): what is over the target is late, keep the newest */
      avail = drop(s, &index, avail);
    }
  } else if (avail > s->target + s->rate * (atomic_load(&s->adaptive) ? 150 : 60) / 1000) {
    avail = drop(s, &index, avail); /* a burst after a stall, or no rate correction */
  }
  uint32_t take = 0;
  if (!s->buffering) {
    take = (uint32_t)avail < n ? (uint32_t)avail : n;
    spa_ringbuffer_read_data(&s->ring, s->data, RING_BYTES, index & (RING_BYTES - 1), out, take * 2);
    spa_ringbuffer_read_update(&s->ring, (int32_t)(index + take * 2));
    if (take < n) {
      s->buffering = 1;
      add(&s->underruns, 1);
    } else {
      adjust(s, avail - (int32_t)take, n);
    }
  }
  memset(out + take, 0, (n - take) * 2);
  add(&s->cycles, 1);
  add(&s->played, take);
  add(&s->silence, n - take);
  atomic_store(&s->fill, avail - (int32_t)take);
  d->chunk->offset = 0;
  d->chunk->stride = 2;
  d->chunk->size = n * 2;
  pw_stream_queue_buffer(s->stream, b);
}

static const struct pw_stream_events source_events = {
    PW_VERSION_STREAM_EVENTS,
    .state_changed = source_state,
    .process = source_process,
};

mic_source *mic_source_new(const char *name, const char *description, int rate, int target, char *error,
                           size_t size) {
  pthread_once(&once, init);
  struct mic_source *s = calloc(1, sizeof(*s));
  if (!s) return NULL;
  s->rate = rate;
  s->target = target;
  s->buffering = 1;
  atomic_init(&s->adaptive, 1);
  atomic_init(&s->correction, 1.0);
  spa_ringbuffer_init(&s->ring);
  s->loop = pw_thread_loop_new("micsource", NULL);
  if (!s->loop || pw_thread_loop_start(s->loop) < 0) {
    snprintf(error, size, "PipeWire: no thread loop");
    mic_source_free(s);
    return NULL;
  }
  char latency[32];
  snprintf(latency, sizeof(latency), "%d/%d", rate / 50, rate);
  pw_thread_loop_lock(s->loop);
  s->stream = pw_stream_new_simple(
      pw_thread_loop_get_loop(s->loop), description,
      pw_properties_new(PW_KEY_MEDIA_TYPE, "Audio", PW_KEY_MEDIA_CLASS, "Audio/Source", PW_KEY_NODE_NAME, name,
                        PW_KEY_NODE_DESCRIPTION, description, PW_KEY_NODE_LATENCY, latency,
                        PW_KEY_DEVICE_ICON_NAME, "audio-input-microphone", NULL),
      &source_events, s);
  int err = -errno;
  if (s->stream) {
    uint8_t buffer[512];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    const struct spa_pod *params[1] = {format(&b, rate)};
    err = pw_stream_connect(s->stream, PW_DIRECTION_OUTPUT, PW_ID_ANY,
                            PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS, params, 1);
  }
  pw_thread_loop_unlock(s->loop);
  if (!s->stream || err < 0) {
    snprintf(error, size, "PipeWire: %s", strerror(-err));
    mic_source_free(s);
    return NULL;
  }
  return s;
}

int mic_source_wait(mic_source *s, int timeout_ms, char *error, size_t size) {
  pw_thread_loop_lock(s->loop);
  int st = wait_state(s->loop, &s->state, timeout_ms);
  int ok = st == PW_STREAM_STATE_PAUSED || st == PW_STREAM_STATE_STREAMING;
  if (!ok)
    snprintf(error, size, "PipeWire: %s", st == PW_STREAM_STATE_ERROR ? s->error : "no node (is PipeWire running?)");
  pw_thread_loop_unlock(s->loop);
  return ok;
}

int mic_source_write(mic_source *s, const int16_t *pcm, int n) {
  if (atomic_load(&s->state) != PW_STREAM_STATE_STREAMING || n <= 0) return 0;
  uint32_t index;
  int32_t filled = spa_ringbuffer_get_write_index(&s->ring, &index);
  if (filled < 0 || filled + n * 2 > RING_BYTES) {
    add(&s->overruns, 1);
    return 0;
  }
  spa_ringbuffer_write_data(&s->ring, s->data, RING_BYTES, index & (RING_BYTES - 1), pcm, (uint32_t)n * 2);
  spa_ringbuffer_write_update(&s->ring, (int32_t)(index + (uint32_t)n * 2));
  return n;
}

void mic_source_get_stats(mic_source *s, mic_source_stats *out) {
  pw_thread_loop_lock(s->loop);
  out->node_id = s->stream ? pw_stream_get_node_id(s->stream) : SPA_ID_INVALID;
  pw_thread_loop_unlock(s->loop);
  out->state = atomic_load(&s->state);
  out->fill = atomic_load(&s->fill);
  out->rate = atomic_load(&s->correction);
  out->adaptive = atomic_load(&s->adaptive);
  out->cycles = atomic_load(&s->cycles);
  out->played = atomic_load(&s->played);
  out->silence = atomic_load(&s->silence);
  out->underruns = atomic_load(&s->underruns);
  out->overruns = atomic_load(&s->overruns);
  out->skipped = atomic_load(&s->skipped);
}

void mic_source_free(mic_source *s) {
  if (!s) return;
  if (s->loop) {
    pw_thread_loop_lock(s->loop);
    if (s->stream) pw_stream_destroy(s->stream);
    pw_thread_loop_unlock(s->loop);
    pw_thread_loop_stop(s->loop);
    pw_thread_loop_destroy(s->loop);
  }
  free(s);
}

/* ------------------------------------------------------------ the capture */

/* Records from the target node only: never from another source if it is gone. */

struct mic_capture {
  struct pw_thread_loop *loop;
  struct pw_stream *stream;
  struct spa_ringbuffer ring;
  uint8_t data[RING_BYTES];
  atomic_int state;
};

static void capture_state(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
  struct mic_capture *c = data;
  (void)old;
  (void)error;
  atomic_store(&c->state, state);
  pw_thread_loop_signal(c->loop, false);
}

static void capture_process(void *data) {
  struct mic_capture *c = data;
  struct pw_buffer *b = pw_stream_dequeue_buffer(c->stream);
  if (!b) return;
  struct spa_data *d = &b->buffer->datas[0];
  if (d->data && d->chunk->size > 0 && d->chunk->offset + d->chunk->size <= d->maxsize) {
    uint32_t index;
    int32_t filled = spa_ringbuffer_get_write_index(&c->ring, &index);
    if (filled >= 0 && filled + (int32_t)d->chunk->size <= RING_BYTES) {
      spa_ringbuffer_write_data(&c->ring, c->data, RING_BYTES, index & (RING_BYTES - 1),
                                SPA_PTROFF(d->data, d->chunk->offset, void), d->chunk->size);
      spa_ringbuffer_write_update(&c->ring, (int32_t)(index + d->chunk->size));
    }
  }
  pw_stream_queue_buffer(c->stream, b);
}

static const struct pw_stream_events capture_events = {
    PW_VERSION_STREAM_EVENTS,
    .state_changed = capture_state,
    .process = capture_process,
};

mic_capture *mic_capture_new(const char *target, int rate, char *error, size_t size) {
  pthread_once(&once, init);
  struct mic_capture *c = calloc(1, sizeof(*c));
  if (!c) return NULL;
  spa_ringbuffer_init(&c->ring);
  c->loop = pw_thread_loop_new("micsource-check", NULL);
  if (!c->loop || pw_thread_loop_start(c->loop) < 0) {
    snprintf(error, size, "PipeWire: no thread loop");
    mic_capture_free(c);
    return NULL;
  }
  char latency[32];
  snprintf(latency, sizeof(latency), "%d/%d", rate / 50, rate);
  pw_thread_loop_lock(c->loop);
  c->stream = pw_stream_new_simple(
      pw_thread_loop_get_loop(c->loop), "micsource check",
      pw_properties_new(PW_KEY_MEDIA_TYPE, "Audio", PW_KEY_MEDIA_CATEGORY, "Capture", PW_KEY_MEDIA_ROLE,
                        "Production", PW_KEY_TARGET_OBJECT, target, "node.dont-fallback", "true",
                        PW_KEY_NODE_DONT_RECONNECT, "true", PW_KEY_NODE_LATENCY, latency, NULL),
      &capture_events, c);
  int err = -errno;
  if (c->stream) {
    uint8_t buffer[512];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    const struct spa_pod *params[1] = {format(&b, rate)};
    err = pw_stream_connect(c->stream, PW_DIRECTION_INPUT, PW_ID_ANY,
                            PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS |
                                PW_STREAM_FLAG_DONT_RECONNECT,
                            params, 1);
  }
  int st = err < 0 ? PW_STREAM_STATE_ERROR : wait_state(c->loop, &c->state, 3000);
  pw_thread_loop_unlock(c->loop);
  if (!c->stream || err < 0 || (st != PW_STREAM_STATE_PAUSED && st != PW_STREAM_STATE_STREAMING)) {
    snprintf(error, size, "PipeWire: no capture stream (%s)", err < 0 ? strerror(-err) : "no link");
    mic_capture_free(c);
    return NULL;
  }
  return c;
}

int mic_capture_state(mic_capture *c) { return atomic_load(&c->state); }

int mic_capture_read(mic_capture *c, int16_t *pcm, int max) {
  uint32_t index;
  int32_t avail = spa_ringbuffer_get_read_index(&c->ring, &index) / 2;
  int n = avail < max ? avail : max;
  if (n <= 0) return 0;
  spa_ringbuffer_read_data(&c->ring, c->data, RING_BYTES, index & (RING_BYTES - 1), pcm, (uint32_t)n * 2);
  spa_ringbuffer_read_update(&c->ring, (int32_t)(index + (uint32_t)n * 2));
  return n;
}

void mic_capture_free(mic_capture *c) {
  if (!c) return;
  if (c->loop) {
    pw_thread_loop_lock(c->loop);
    if (c->stream) pw_stream_destroy(c->stream);
    pw_thread_loop_unlock(c->loop);
    pw_thread_loop_stop(c->loop);
    pw_thread_loop_destroy(c->loop);
  }
  free(c);
}
