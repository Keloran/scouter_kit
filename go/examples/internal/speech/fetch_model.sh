#!/bin/sh
# Fetches speech models at pinned revisions into DIR and checks their SHA-256: Whisper models in whisper.cpp's
# format (MIT, huggingface.co/ggerganov/whisper.cpp) as DIR/ggml-MODEL.bin, the Silero voice activity model (MIT,
# huggingface.co/ggml-org/whisper-vad) as DIR/ggml-silero-*.bin, and streaming models for sherpa-onnx (its
# asr-models release) unpacked into DIR/NAME/.
# Usage: fetch_model.sh DIR [MODEL...]   (default: small-q5_1 silero-v6.2.0 fastconformer-en-80ms)
# MODEL: tiny tiny.en base base.en base-q5_1 small small.en small-q5_1 medium-q5_0 large-v3-turbo-q5_0 silero-v6.2.0
#        fastconformer-en-80ms fastconformer-en-480ms (NVIDIA NeMo FastConformer streaming transducers, CC-BY-4.0)
#        kokoro-en-int8 (Kokoro v0.19 text to speech, Apache-2.0: natural test speech for the tests only)
set -eu
WHISPER=https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1
VAD=https://huggingface.co/ggml-org/whisper-vad/resolve/9ffd54a1e1ee413ddf265af9913beaf518d1639b
SHERPA=https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models
sha() {
	case $1 in
	tiny) echo be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21 ;;
	tiny.en) echo 921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f ;;
	base) echo 60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe ;;
	base.en) echo a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002 ;;
	base-q5_1) echo 422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898 ;;
	small) echo 1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b ;;
	small.en) echo c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d ;;
	small-q5_1) echo ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb ;;
	medium-q5_0) echo 19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f ;;
	large-v3-turbo-q5_0) echo 394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2 ;;
	silero-v6.2.0) echo 2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987 ;;
	fastconformer-en-80ms) echo 7bd33a914e93370a1ba9c2066d9e841bdcad8613fa2a00537c1ae15d851a14d8 ;;
	fastconformer-en-480ms) echo da93061cbf7b708b6b65976f70b29f519be29df750d8cdcabf98c65645930f13 ;;
	kokoro-en-int8) echo c9f0dd393615805b0bab050c340834d5e684e732aec91c0e860cd30e982c08bd ;;
	*) return 1 ;;
	esac
}
archive() {
	case $1 in
	fastconformer-en-*) echo "sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-${1#fastconformer-en-}-int8" ;;
	kokoro-en-int8) echo kokoro-int8-en-v0_19 ;;
	*) return 1 ;;
	esac
}
[ $# -ge 1 ] || { echo "usage: fetch_model.sh DIR [MODEL...]" >&2; exit 2; }
DIR=$1
shift
[ $# -gt 0 ] || set -- small-q5_1 silero-v6.2.0 fastconformer-en-80ms
for m; do sha "$m" >/dev/null || { echo "fetch_model.sh: unknown model $m" >&2; exit 2; }; done
mkdir -p "$DIR"
for m; do
	if a=$(archive "$m"); then
		if [ -d "$DIR/$a" ]; then
			echo "$DIR/$a: already there"
			continue
		fi
		url=$SHERPA/$a.tar.bz2
		case $m in kokoro-*) url=https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/$a.tar.bz2 ;; esac
		curl -fsSL --proto '=https' -o "$DIR/$a.tar.bz2.part" "$url"
		echo "$(sha "$m")  $DIR/$a.tar.bz2.part" | sha256sum -c -
		tar xjf "$DIR/$a.tar.bz2.part" -C "$DIR"
		rm "$DIR/$a.tar.bz2.part"
		echo "$DIR/$a"
		continue
	fi
	f=$DIR/ggml-$m.bin
	if [ -f "$f" ] && echo "$(sha "$m")  $f" | sha256sum -c --status -; then
		echo "$f: already there"
		continue
	fi
	case $m in
	silero-*) url=$VAD/ggml-$m.bin ;;
	*) url=$WHISPER/ggml-$m.bin ;;
	esac
	curl -fsSL --proto '=https' -o "$f.part" "$url"
	echo "$(sha "$m")  $f.part" | sha256sum -c -
	mv "$f.part" "$f"
done
