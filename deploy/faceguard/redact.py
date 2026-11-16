#!/usr/bin/env python3
"""参考视频人脸打码：检测主体（或全部）人脸并像素化，降低上游换脸审核拒绝率。

CLI:
  redact.py --input in.mp4 --output out.mp4 [--mode subject|all] [--max-faces N]
            [--sample 2] [--pad 0.22] [--blocks 9] [--debug-json path]

mode=subject 只打画面主体（面积+居中加权最大的一张脸），mode=all 打全部可检人脸。
sample=N 表示每 N 帧做一次检测、中间帧沿用前一帧框。检测在缩小到 480 宽的帧上做，
坐标按比例映射回原尺寸，精度损失可忽略但速度快 2 倍以上。
"""
import argparse
import json
import os
import subprocess
import sys

import cv2
import numpy as np

MODEL = os.environ.get('FACEGUARD_YUNET', '/opt/faceguard/yunet.onnx')
DETECT_WIDTH = 480
FFMPEG = os.environ.get('FACEGUARD_FFMPEG', '/usr/bin/ffmpeg')


def pick_subject(boxes, frame_w, frame_h, max_faces):
    if not boxes:
        return []
    cx, cy = frame_w / 2.0, frame_h / 2.0
    diag = ((cx ** 2 + cy ** 2) ** 0.5) or 1.0

    def score(b):
        x, y, w, h, conf = b
        dist = (((x + w / 2 - cx) ** 2 + (y + h / 2 - cy) ** 2) ** 0.5) / diag
        return w * h * (1.0 - 0.35 * min(1.0, dist)) * (0.6 + 0.4 * conf)

    return sorted(boxes, key=lambda b: -score(b))[:max_faces]


def mosaic(img, box, pad, blocks):
    h, w = img.shape[:2]
    x, y, bw, bh = box
    px, py = bw * pad, bh * pad
    x0, y0 = max(0, int(x - px)), max(0, int(y - py))
    x1, y1 = min(w, int(x + bw + px)), min(h, int(y + bh + py))
    if x1 - x0 < 4 or y1 - y0 < 4:
        return
    roi = img[y0:y1, x0:x1]
    tw, th = max(2, (x1 - x0) // blocks), max(2, (y1 - y0) // blocks)
    small = cv2.resize(roi, (tw, th), interpolation=cv2.INTER_AREA)
    up = cv2.resize(small, (x1 - x0, y1 - y0), interpolation=cv2.INTER_NEAREST)
    # 像素化后再过一道自适应高斯，把五官边缘彻底抹掉，避免上游仍能靠轮廓识别人脸。
    sigma = max(1.2, min(x1 - x0, y1 - y0) / 16.0)
    img[y0:y1, x0:x1] = cv2.GaussianBlur(up, (0, 0), sigma)


def detect_frame(detector, frame):
    h, w = frame.shape[:2]
    if w > DETECT_WIDTH:
        scale = DETECT_WIDTH / float(w)
        small = cv2.resize(frame, (DETECT_WIDTH, max(1, int(h * scale))), interpolation=cv2.INTER_AREA)
    else:
        scale = 1.0
        small = frame
    detector.setInputSize((small.shape[1], small.shape[0]))
    _, faces = detector.detect(small)
    if faces is None:
        return []
    inv = 1.0 / scale
    return [(float(f[0]) * inv, float(f[1]) * inv, float(f[2]) * inv, float(f[3]) * inv, float(f[-1])) for f in faces]


def redact_video(input_path, output_path, mode='subject', max_faces=1, sample=2,
                 pad=0.22, blocks=9, smooth=0.6, debug_json=''):
    cap = cv2.VideoCapture(input_path)
    if not cap.isOpened():
        raise RuntimeError('无法打开输入视频')
    fps = cap.get(cv2.CAP_PROP_FPS) or 25.0
    w = int(cap.get(cv2.CAP_PROP_FRAME_WIDTH))
    h = int(cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
    detector = cv2.FaceDetectorYN.create(MODEL, '', (w, h), 0.55, 0.3, 1000)

    enc = subprocess.Popen([
        FFMPEG, '-hide_banner', '-loglevel', 'error', '-nostdin', '-y',
        '-f', 'rawvideo', '-pix_fmt', 'bgr24', '-s', f'{w}x{h}', '-r', f'{fps:.6f}', '-i', '-',
        '-i', input_path,
        '-map', '0:v:0', '-map', '1:a:0?',
        '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '19', '-pix_fmt', 'yuv420p',
        '-profile:v', 'high', '-level', '4.1',
        '-c:a', 'aac', '-b:a', '128k', '-shortest', '-movflags', '+faststart', output_path,
    ], stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)

    idx = 0
    detected = {}
    last, last_idx = None, -999
    smooth_boxes = None
    stats = {'frames': 0, 'frames_masked': 0, 'detect_calls': 0, 'faces_found': 0}
    try:
        while True:
            ok, frame = cap.read()
            if not ok:
                break
            if idx % max(1, sample) == 0:
                stats['detect_calls'] += 1
                boxes = detect_frame(detector, frame)
                if mode == 'all':
                    boxes = sorted(boxes, key=lambda b: -b[4])[:max_faces]
                else:
                    boxes = pick_subject(boxes, w, h, max_faces)
                if boxes:
                    stats['faces_found'] += 1
                    last, last_idx = boxes, idx
                elif mode == 'subject' and last and idx - last_idx <= 4:
                    boxes = last
                else:
                    boxes = []
                detected[idx] = boxes
            boxes = detected.get(idx) or (last if (mode == 'subject' and last and idx - last_idx <= 4) else [])
            if boxes:
                stats['frames_masked'] += 1
                if smooth_boxes is None or len(smooth_boxes) != len(boxes):
                    smooth_boxes = [list(b[:4]) for b in boxes]
                else:
                    for j, b in enumerate(boxes):
                        for k in range(4):
                            smooth_boxes[j][k] = smooth * smooth_boxes[j][k] + (1 - smooth) * b[k]
                for j in range(len(boxes)):
                    mosaic(frame, smooth_boxes[j], pad, blocks)
            else:
                smooth_boxes = None
            enc.stdin.write(np.ascontiguousarray(frame).tobytes())
            idx += 1
    finally:
        cap.release()
        try:
            enc.stdin.close()
        except Exception:
            pass
        err = enc.stderr.read().decode('utf-8', 'ignore')
        code = enc.wait()
    if code != 0:
        raise RuntimeError('编码失败：' + err[-400:])

    stats['frames'] = idx
    stats['output_bytes'] = os.path.getsize(output_path) if os.path.exists(output_path) else 0
    stats['mode'] = mode
    stats['sample'] = sample
    if debug_json:
        json.dump({str(k): v for k, v in detected.items()}, open(debug_json, 'w'))
    return stats


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--input', required=True)
    ap.add_argument('--output', required=True)
    ap.add_argument('--mode', default='subject', choices=['subject', 'all'])
    ap.add_argument('--max-faces', type=int, default=1)
    ap.add_argument('--sample', type=int, default=2)
    ap.add_argument('--pad', type=float, default=0.22)
    ap.add_argument('--blocks', type=int, default=9)
    ap.add_argument('--smooth', type=float, default=0.6)
    ap.add_argument('--debug-json', default='')
    args = ap.parse_args()
    stats = redact_video(args.input, args.output, args.mode, args.max_faces, args.sample,
                         args.pad, args.blocks, args.smooth, args.debug_json)
    print(json.dumps(stats, ensure_ascii=False))
    return 0


if __name__ == '__main__':
    sys.exit(main())
