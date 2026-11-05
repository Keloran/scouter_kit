/* PipeWire for micsource: an Audio/Source node that plays samples queued by
   the application (16-bit mono), and a capture stream that records from a
   node, for checks. Each has its own thread loop. */
#ifndef MICSOURCE_PIPEWIRE_H
#define MICSOURCE_PIPEWIRE_H

#include <stddef.h>
#include <stdint.h>

typedef struct mic_source mic_source;
typedef struct mic_capture mic_capture;

typedef struct {
  int state; /* enum pw_stream_state: -1 error, 0 unconnected, 1 connecting,
                2 paused, 3 streaming (something records from the node) */
  uint32_t node_id;
  int fill;      /* samples queued */
  double rate;   /* the correction given to the stream's resampler */
  int adaptive;  /* the stream takes rate corrections */
  uint64_t cycles, played, silence, underruns, overruns, skipped;
} mic_source_stats;

/* target: samples kept queued, the latency the node adds. */
mic_source *mic_source_new(const char *name, const char *description,
                           int rate, int target, char *error, size_t size);
/* 1 once the node exists, else 0 (after timeout_ms or an error). */
int mic_source_wait(mic_source *s, int timeout_ms, char *error, size_t size);
/* Queues samples; while nothing records from the node they are dropped. */
int mic_source_write(mic_source *s, const int16_t *pcm, int n);
void mic_source_get_stats(mic_source *s, mic_source_stats *stats);
/* Removes the node. */
void mic_source_free(mic_source *s);

mic_capture *mic_capture_new(const char *target, int rate, char *error,
                             size_t size);
int mic_capture_state(mic_capture *c);
/* Up to max samples recorded since the last read. */
int mic_capture_read(mic_capture *c, int16_t *pcm, int max);
void mic_capture_free(mic_capture *c);

#endif
