#!/bin/sh
# ffmpeg.exe-аналоги для Android (arm64): ffprobe для сведений о файле и ffmpeg для потока
# «Смотреть» (remux в Matroska, звук в AAC). Перекода видео в VP8 на Android нет — Media3
# декодирует MPEG-4 сам, а компоненты — как у исходной LGPL-сборки third_party/ffmpeg/build.sh
# до libvpx (план 2026-10-06, полный порт сервера). Запуск из MSYS2:
#   sh third_party/ffmpeg/build-android.sh
set -eu
VERSION=8.1.3
HERE="$(cd "$(dirname "$0")" && pwd)"
NDK="${ANDROID_NDK_HOME:-$LOCALAPPDATA/Android/Sdk/ndk/28.2.13676358}"
case "$NDK" in
  [A-Za-z]:*) NDK="$(cygpath -u "$NDK")";; # C:\… → /c/…
esac
TC="$NDK/toolchains/llvm/prebuilt/windows-x86_64/bin"
API=24
[ -x "$TC/clang.exe" ] || { echo "нет NDK: $TC"; exit 1; }

# Обёртки clang: configure зовёт компилятор одним словом.
WRAP="$(mktemp -d)"
for t in aarch64-linux-android"$API"; do
  printf '#!/bin/sh\nexec "%s/clang.exe" --target=%s "$@"\n' "$TC" "$t" > "$WRAP/$t-clang"
  chmod +x "$WRAP/$t-clang"
done
export PATH="$WRAP:/mingw64/bin:$PATH"

cd "$HERE"
mkdir -p android
# Исходники — внутри android/src: у MSYS2 и Git Bash разные /tmp, детерминированность важнее.
SRC="$HERE/android/src/ffmpeg-$VERSION"
rm -rf "$HERE/android/src"
mkdir -p "$(dirname "$SRC")"
curl -fsSLO "https://ffmpeg.org/releases/ffmpeg-$VERSION.tar.xz"
tar xf "ffmpeg-$VERSION.tar.xz" -C "$(dirname "$SRC")"
cd "$SRC"
./configure --target-os=android --arch=aarch64 --enable-cross-compile \
  --cc="aarch64-linux-android$API-clang" --host-cc=/mingw64/bin/gcc --ar="$TC/llvm-ar.exe" --ranlib="$TC/llvm-ranlib.exe" \
  --strip="$TC/llvm-strip.exe" --nm="$TC/llvm-nm.exe" \
  --enable-static --disable-shared --disable-debug --disable-doc --disable-programs \
  --enable-ffmpeg --enable-ffprobe \
  --disable-autodetect --disable-w32threads --enable-pthreads --disable-x86asm --disable-everything \
  --disable-avdevice --disable-swscale \
  --enable-avformat --enable-avcodec --enable-avfilter --enable-swresample --enable-network \
  --enable-protocol=file,http,pipe,tcp --enable-demuxer=matroska,mov,avi,mpegts,srt,ass,webvtt \
  --enable-muxer=mpegts,matroska,webvtt --enable-decoder=h264,hevc,ac3,eac3,dca,truehd,mlp,mp3,mp3float,mp2,mp1,mp1float,alac,wmav1,wmav2,wmapro,aac,aac_latm,flac,opus,vorbis,pcm_s16le,pcm_s24le,pcm_s32le,pcm_f32le,pcm_s16be,pcm_bluray,pcm_dvd,subrip,srt,ass,ssa,webvtt,movtext,text \
  --enable-encoder=aac,webvtt,subrip,ass --enable-parser=h264,hevc,aac,aac_latm,ac3,dca,mpegaudio,flac,opus,vorbis,mlp \
  --enable-bsf=aac_adtstoasc,extract_extradata,h264_mp4toannexb,hevc_mp4toannexb \
  --enable-filter=aresample,aformat,anull,atrim > configure-android.log 2>&1 || { tail -30 configure-android.log; exit 1; }
check() {
  for c in $(echo "$2" | tr , ' '); do
    name=$(echo "${c}_$1" | tr a-z A-Z)
    grep -q "^#define CONFIG_$name 1" config_components.h || { echo "компонент не включён: $c ($1)"; exit 1; }
  done
}
check protocol file,http,pipe,tcp
check demuxer matroska,mov,avi,mpegts,srt,ass,webvtt
check muxer mpegts,matroska,webvtt
check decoder h264,hevc,ac3,eac3,aac
check encoder aac
check parser h264,hevc,aac,ac3
check bsf aac_adtstoasc,extract_extradata
check filter aresample,aformat
make -j"$(nproc)" ffmpeg ffprobe > make-android.log 2>&1 || { tail -40 make-android.log; exit 1; }
"$TC/llvm-strip.exe" ffmpeg ffprobe
cp ffmpeg ffprobe "$HERE/android/"
ls -la "$HERE/android/"
