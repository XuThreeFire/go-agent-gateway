#!/usr/bin/env bash
# mp4-to-gif.sh —— 把录屏/演示视频转成体积可控、颜色不失真的 GIF
#
# 用法:
#   ./scripts/mp4-to-gif.sh                     # 默认转当前目录的 演示视频.mp4
#   ./scripts/mp4-to-gif.sh input.mp4           # 指定输入，输出同名 .gif
#   ./scripts/mp4-to-gif.sh input.mp4 out.gif   # 指定输入输出
#
# 可调参数(环境变量):
#   WIDTH=960      输出宽度(高度按比例,必须偶数)
#   FPS=12         抽帧帧率,越低越小
#   SPEED=1.0      播放倍速,2.0=两倍速(同时会把帧数减半,明显减体积)
#   START=0        起始时间(秒或 HH:MM:SS)
#   DURATION=      截取时长(秒),留空=整段
#   COLORS=256     调色板颜色数(128 更小)
#   DITHER=bayer   抖动算法: bayer(规整纹路) / sierra2_4a(更细腻但更大) / none
#   CROP_BOTTOM=0  从底部裁掉多少(去编辑器状态栏/桌面/水印)。支持像素 200 或百分比 10%
#   CROP_TOP=0     从顶部裁掉多少,格式同上
#   CROP=          完全自定义裁剪,直接给 ffmpeg 的 w:h:x:y(优先级最高)
#
# 示例:
#   WIDTH=720 FPS=10 SPEED=2 ./scripts/mp4-to-gif.sh 演示视频.mp4 demo-speed2.gif
#   START=5 DURATION=10 ./scripts/mp4-to-gif.sh 演示视频.mp4 clip.gif
#   CROP_BOTTOM=10% ./scripts/mp4-to-gif.sh            # 裁掉底部 10%(去隐私信息)
set -euo pipefail

INPUT="${1:-演示视频.mp4}"
OUTPUT="${2:-${INPUT%.*}.gif}"

WIDTH="${WIDTH:-960}"
FPS="${FPS:-12}"
SPEED="${SPEED:-1.0}"
START="${START:-0}"
DURATION="${DURATION:-}"
COLORS="${COLORS:-256}"
DITHER="${DITHER:-bayer}"
CROP_TOP="${CROP_TOP:-0}"
CROP_BOTTOM="${CROP_BOTTOM:-0}"
CROP="${CROP:-}"

command -v ffmpeg >/dev/null 2>&1 || { echo "错误: 未找到 ffmpeg,请先 brew install ffmpeg" >&2; exit 1; }
[ -f "$INPUT" ] || { echo "错误: 输入文件不存在: $INPUT" >&2; exit 1; }

mkdir -p "$(dirname "$OUTPUT")"

# 输入侧裁剪参数
trim=(-ss "$START")
[ -n "$DURATION" ] && trim+=(-t "$DURATION")

# 把 "200" 或 "10%" 翻译成 ffmpeg crop 表达式里的高度项
len_expr() {
  case "$1" in
    0|"") echo "0" ;;
    *%) echo "ih*${1%\%}/100" ;;
    *) echo "$1" ;;
  esac
}

# 裁边:必须在 scale 之前,按原始像素坐标裁,否则算出来的位置会随缩放漂移
crop=""
if [ -n "$CROP" ]; then
  crop="crop=${CROP},"
else
  top=$(len_expr "$CROP_TOP")
  bot=$(len_expr "$CROP_BOTTOM")
  if [ "$top" = "0" ] && [ "$bot" != "0" ]; then
    crop="crop=iw:ih-${bot}:0:0,"
  elif [ "$top" != "0" ] && [ "$bot" = "0" ]; then
    crop="crop=iw:ih-${top}:0:${top},"
  elif [ "$top" != "0" ] && [ "$bot" != "0" ]; then
    crop="crop=iw:ih-${top}-${bot}:0:${top},"
  fi
fi

# 缩放:宽度取偶数,高度按 -2 保证偶数对齐
scale="scale=${WIDTH}:-2:flags=lanczos"

# 帧率滤镜(先抽帧),再按 SPEED 压缩时间轴 —— 顺序很重要:
# 先 fps 后 setpts 才能真正减少帧数,反过来只是加快播放、体积不变
rate="fps=${FPS}"
[ "$SPEED" != "1.0" ] && rate="${rate},setpts=PTS/${SPEED}"

echo "==> 输入: $INPUT"
echo "==> 输出: $OUTPUT (宽 ${WIDTH}px, ${FPS}fps, 倍速 ${SPEED}x, ${COLORS} 色, 抖动 ${DITHER})"
[ -n "$crop" ] && echo "==> 裁边: $crop"

ffmpeg -hide_banner -loglevel error -stats -y \
  "${trim[@]}" -i "$INPUT" \
  -filter_complex "[0:v]${crop}${scale},${rate},split[a][b];\
[a]palettegen=max_colors=${COLORS}:stats_mode=diff[p];\
[b][p]paletteuse=dither=${DITHER}:bayer_scale=4:diff_mode=rectangle" \
  -loop 0 "$OUTPUT"

# 有 gifsicle 就再压一轮(本地 FFmpeg 的 gif 编码不如 gifsicle 的帧间差分优化)
if command -v gifsicle >/dev/null 2>&1; then
  echo "==> gifsicle 二次优化"
  gifsicle -O3 --lossy=60 --colors "$COLORS" -o "$OUTPUT" "$OUTPUT"
else
  echo "==> 提示: 未安装 gifsicle,跳过二次优化(brew install gifsicle 可再省 20-40% 体积)"
fi

size=$(ls -lh "$OUTPUT" | awk '{print $5}')
echo "==> 完成: $OUTPUT ($size)"
