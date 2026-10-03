#!/bin/sh
# Урезанные ffmpeg.exe и ffprobe.exe для Kinodom (LGPL, без --enable-gpl и --enable-nonfree): кросс-сборка
# mingw-w64 в Debian. Запуск из этой папки (Docker Desktop, Git Bash):
#   MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -w "$PWD"):/out" -v "$(cygpath -w "$PWD/build.sh"):/build.sh:ro" debian:bookworm sh /build.sh
# Внутри — только то, что нужно плееру (спека цикла 18, раздел 3.1).
set -eu
VERSION=8.1.3
PROTOCOLS=file,http,pipe,tcp
DEMUXERS=matroska,mov,avi,mpegts,srt,ass,webvtt
MUXERS=mpegts,matroska,webvtt
DECODERS=h264,hevc,ac3,eac3,dca,truehd,mlp,mp3,mp3float,mp2,mp1,mp1float,alac,wmav1,wmav2,wmapro,aac,aac_latm,flac,opus,vorbis,pcm_s16le,pcm_s24le,pcm_s32le,pcm_f32le,pcm_s16be,pcm_bluray,pcm_dvd,subrip,srt,ass,ssa,webvtt,movtext,text
ENCODERS=aac,webvtt,subrip,ass
PARSERS=h264,hevc,aac,aac_latm,ac3,dca,mpegaudio,flac,opus,vorbis,mlp
BSFS=aac_adtstoasc,extract_extradata,h264_mp4toannexb,hevc_mp4toannexb
FILTERS=aresample,aformat,anull,atrim # abuffer и abuffersink собираются всегда, это не компоненты configure
APT='-o Acquire::http::Timeout=30 -o Acquire::Retries=5'
apt-get $APT update -qq >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get $APT install -y -qq --no-install-recommends gcc libc6-dev mingw-w64 make pkg-config curl ca-certificates xz-utils >/dev/null
cd /tmp
curl -fsSLO "https://ffmpeg.org/releases/ffmpeg-$VERSION.tar.xz"
sha256sum "ffmpeg-$VERSION.tar.xz" | tee /out/source.sha256
tar xf "ffmpeg-$VERSION.tar.xz"
cd "ffmpeg-$VERSION"
./configure --arch=x86_64 --target-os=mingw32 --cross-prefix=x86_64-w64-mingw32- \
  --enable-static --disable-shared --disable-debug --disable-doc \
  --disable-autodetect --enable-w32threads --disable-x86asm --disable-everything \
  --disable-ffplay --disable-avdevice --disable-swscale \
  --enable-ffmpeg --enable-ffprobe --enable-avformat --enable-avcodec --enable-avfilter --enable-swresample --enable-network \
  --enable-protocol=$PROTOCOLS --enable-demuxer=$DEMUXERS --enable-muxer=$MUXERS --enable-decoder=$DECODERS \
  --enable-encoder=$ENCODERS --enable-parser=$PARSERS --enable-bsf=$BSFS --enable-filter=$FILTERS \
  --extra-ldflags=-static > /out/configure.log 2>&1 || { tail -30 /out/configure.log; exit 1; }
# configure молча пропускает неверное имя компонента (так пропал «mov_text» вместо «movtext», ревью 18А) — проверить каждый.
check() {
  for c in $(echo "$2" | tr , ' '); do
    name=$(echo "${c}_$1" | tr a-z A-Z)
    grep -q "^#define CONFIG_$name 1" config_components.h || { echo "компонент не включён: $c ($1)"; exit 1; }
  done
}
check protocol $PROTOCOLS
check demuxer $DEMUXERS
check muxer $MUXERS
check decoder $DECODERS
check encoder $ENCODERS
check parser $PARSERS
check bsf $BSFS
check filter $FILTERS
make -j"$(nproc)" ffmpeg.exe ffprobe.exe > /out/make.log 2>&1 || { tail -40 /out/make.log; exit 1; }
x86_64-w64-mingw32-strip ffmpeg.exe ffprobe.exe
cp ffmpeg.exe ffprobe.exe /out/
cp COPYING.LGPLv2.1 /out/LICENSE.LGPLv2.1.txt
rm -f /out/configure.log /out/make.log
ls -la /out/*.exe
