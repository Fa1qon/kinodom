#!/bin/sh
# Урезанные ffmpeg.exe и ffprobe.exe для Kinodom (LGPL, без --enable-gpl и --enable-nonfree): кросс-сборка
# mingw-w64 в Debian. Запуск из этой папки (Docker Desktop, Git Bash):
#   MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -w "$PWD"):/out" -v "$(cygpath -w "$PWD/build.sh"):/build.sh:ro" debian:bookworm sh /build.sh
# Тот же скрипт — в GitHub Actions (.github/workflows/ffmpeg.yml). Внутри — только то, что нужно плееру
# (спека цикла 18, раздел 3.1; запасной путь с перекодировкой видео — план 2026-10-06).
# libvpx (VP8/VP9) — BSD, единственный браузеро-совместимый энкодер видео, чистый для LGPL-сборки:
# H.264/HEVC — только GPL-библиотеки, openh264 без патентной лицензии Cisco распространять нельзя.
set -eu
VERSION=8.1.3
VPX=1.15.0
OPUS=1.5.2
PROTOCOLS=file,http,pipe,tcp
DEMUXERS=matroska,mov,avi,mpegts,srt,ass,webvtt
MUXERS=mpegts,matroska,webvtt,webm,hls,mp4
DECODERS=h264,hevc,mpeg4,msmpeg4v3,h263,ac3,eac3,dca,truehd,mlp,mp3,mp3float,mp2,mp1,mp1float,alac,wmav1,wmav2,wmapro,aac,aac_latm,flac,opus,vorbis,pcm_s16le,pcm_s24le,pcm_s32le,pcm_f32le,pcm_s16be,pcm_bluray,pcm_dvd,subrip,srt,ass,ssa,webvtt,movtext,text
ENCODERS=aac,webvtt,subrip,ass,libvpx_vp8,libvpx_vp9,libopus
PARSERS=h264,hevc,mpeg4video,h263,aac,aac_latm,ac3,dca,mpegaudio,flac,opus,vorbis,mlp
BSFS=aac_adtstoasc,extract_extradata,h264_mp4toannexb,hevc_mp4toannexb
FILTERS=aresample,aformat,anull,atrim,scale,format # abuffer и abuffersink собираются всегда, это не компоненты configure
APT='-o Acquire::http::Timeout=30 -o Acquire::Retries=5'
apt-get $APT update -qq >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends gcc libc6-dev mingw-w64 make pkg-config curl ca-certificates xz-utils nasm >/dev/null
cd /tmp
# libvpx и libopus для mingw: Debian собирает их под Linux — под Windows только из исходников
# (лицензии BSD — чисто для LGPL-сборки).
curl -fsSLO "https://github.com/webmproject/libvpx/archive/refs/tags/v$VPX.tar.gz"
echo "e935eded7d81631a538bfae703fd1e293aad1c7fd3407ba00440c95105d2011e  v$VPX.tar.gz" | sha256sum -c -
tar xf "v$VPX.tar.gz"
cd "libvpx-$VPX"
LDFLAGS=-static ./configure --target=x86_64-win64-gcc --prefix=/usr/local \
  --disable-shared --enable-static --disable-unit-tests --disable-docs --disable-examples --disable-tools \
  --enable-pic --disable-webm-io > vpx-configure.log 2>&1 || { tail -30 vpx-configure.log; exit 1; }
make -j"$(nproc)" > vpx-make.log 2>&1 || { tail -40 vpx-make.log; exit 1; }
make install > /dev/null
cd /tmp
curl -fsSLO "https://downloads.xiph.org/releases/opus/opus-$OPUS.tar.gz"
echo "65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1  opus-$OPUS.tar.gz" | sha256sum -c -
tar xf "opus-$OPUS.tar.gz"
cd "opus-$OPUS"
./configure --host=x86_64-w64-mingw32 --prefix=/usr/local --disable-shared --enable-static \
  --disable-extra-programs --disable-doc > opus-configure.log 2>&1 || { tail -30 opus-configure.log; exit 1; }
make -j"$(nproc)" > opus-make.log 2>&1 || { tail -40 opus-make.log; exit 1; }
make install > /dev/null
cd /tmp
curl -fsSLO "https://ffmpeg.org/releases/ffmpeg-$VERSION.tar.xz"
sha256sum "ffmpeg-$VERSION.tar.xz" | tee /out/source.sha256
tar xf "ffmpeg-$VERSION.tar.xz"
cd "ffmpeg-$VERSION"
export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig
./configure --arch=x86_64 --target-os=mingw32 --cross-prefix=x86_64-w64-mingw32- \
  --enable-static --disable-shared --disable-debug --disable-doc \
  --disable-autodetect --enable-w32threads --disable-x86asm --disable-everything \
  --disable-ffplay --disable-avdevice \
  --enable-ffmpeg --enable-ffprobe --enable-avformat --enable-avcodec --enable-avfilter --enable-swresample --enable-swscale --enable-network \
  --enable-protocol=$PROTOCOLS --enable-demuxer=$DEMUXERS --enable-muxer=$MUXERS --enable-decoder=$DECODERS \
  --enable-encoder=$ENCODERS --enable-parser=$PARSERS --enable-bsf=$BSFS --enable-filter=$FILTERS \
  --enable-libvpx --extra-cflags=-I/usr/local/include --extra-ldflags="-static -L/usr/local/lib" \
  --pkg-config-flags=--static > /out/configure.log 2>&1 || { tail -30 /out/configure.log; exit 1; }
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
# Лицензии компонентов сверх ffmpeg — рядом (README перечисляет, откуда исходники).
cat > /out/LICENSE.opus.txt <<'EOF'
Copyright 2001-2023 Xiph.Org Foundation and contributors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:

- Redistributions of source code must retain the above copyright
  notice, this list of conditions and the following disclaimer.

- Redistributions in binary form must reproduce the above copyright
  notice, this list of conditions and the following disclaimer in the
  documentation and/or other materials provided with the distribution.

- Neither the name of the Xiph.Org Foundation nor the names of its
  contributors may be used to endorse or promote products derived from
  this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE FOUNDATION
OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
EOF
cat > /out/LICENSE.vpx.txt <<'EOF'
Copyright (c) 2010, The WebM Project authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

1. Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in
   the documentation and/or other materials provided with the
   distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
EOF
rm -f /out/configure.log /out/make.log
ls -la /out/*.exe
