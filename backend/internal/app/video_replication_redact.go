package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 参考视频主体打码：换人任务在上游成片阶段会被内容审核按「真人换脸」拒绝（VIDEO_CONTENT_REVIEW）。
// 把原片里的人脸糊掉后再作为编辑底片，上游识别不到被篡改的真实人物身份，成片只要靠参考图重绘人脸即可。
// 默认只打主体（面积 + 居中加权最大的一张脸），背景路人的表情和朝向信息保留给模型参考。
//
// 打码依赖宿主机上的 faceguard 服务（Python + OpenCV YuNet，见 /opt/faceguard）：
// 容器里没有 python/opencv，而且抽帧检测 + 逐帧重编码属于 CPU 密集任务，放在宿主机上跑更简单。
// 服务不可用时一律降级为「不打码继续提交」，不能因为增强能力故障阻断主链路。

const (
	faceGuardSettingKey        = "face_guard_setting"
	faceGuardDefaultURL        = "http://172.19.0.1:18091"
	faceGuardRequestTimeout    = 5 * time.Minute
	videoReplicationRedactClip = "redact"
)

type faceGuardSetting struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Mode     string `json:"mode"`
	MaxFaces int    `json:"maxFaces"`
	Sample   int    `json:"sample"`
}

func (s faceGuardSetting) normalized() faceGuardSetting {
	if strings.TrimSpace(s.URL) == "" {
		s.URL = faceGuardDefaultURL
	}
	s.URL = strings.TrimRight(s.URL, "/")
	if s.Mode != "all" {
		s.Mode = "subject"
	}
	if s.MaxFaces <= 0 {
		s.MaxFaces = 1
	}
	if s.Sample <= 0 {
		s.Sample = 2
	}
	return s
}

// faceGuardSettings 读取后台配置，缺省即开启（换人成功率优先）；读不到配置按默认值执行。
func (s *Service) faceGuardSettings() faceGuardSetting {
	setting := faceGuardSetting{Enabled: true, URL: faceGuardDefaultURL, Mode: "subject", MaxFaces: 1, Sample: 2}
	record, err := s.repo.SystemSettingOptional(faceGuardSettingKey)
	if err != nil || record == nil || strings.TrimSpace(record.ValueJSON) == "" {
		return setting.normalized()
	}
	if err := json.Unmarshal([]byte(record.ValueJSON), &setting); err != nil {
		return faceGuardSetting{Enabled: true, URL: faceGuardDefaultURL, Mode: "subject", MaxFaces: 1, Sample: 2}.normalized()
	}
	return setting.normalized()
}

// videoReplicationRedactsFace 判断这条任务是否要打码：只有「换人」类任务需要，
// 换商品、纯场景重绘打码只会白白损失画面信息。
// video_type 是逗号分隔的多选值（如 product,model），replace_mode 取值见
// video_replication.go:493（""/model/clothing/face/background/product/precise-product/precise-person/combination）。
func videoReplicationRedactsFace(videoType string, params VideoReplicationParameters) bool {
	switch params.ReplaceMode {
	case "face", "precise-person":
		// 精确框选换人和纯换脸：明确要换真人，必打码。
		return true
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeModel) {
		return true
	}
	return params.ReplaceMode == "model"
}

type faceGuardRequest struct {
	Input    string `json:"input"`
	Output   string `json:"output"`
	Mode     string `json:"mode"`
	MaxFaces int    `json:"maxFaces"`
	Sample   int    `json:"sample"`
}

type faceGuardResponse struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
	MS     int    `json:"ms"`
}

// redactReferenceVideo 把参考视频里的人脸糊掉，返回打码后的文件路径。
// 输入文件通常落在容器内的临时目录（/tmp），宿主机上的打码服务读不到，
// 所以先复制到容器与宿主机共享的数据目录（/data → /opt/open-ai-canvas/data/backend）再处理，
// 处理完立刻删掉输入副本，输出文件由调用方在上传成资源后删除。
func (s *Service) redactReferenceVideo(ctx context.Context, input string) (string, error) {
	setting := s.faceGuardSettings()
	if !setting.Enabled {
		return "", fmt.Errorf("人脸打码已关闭")
	}
	shared := filepath.Join(s.dataDir, "tmp", "faceguard")
	if err := os.MkdirAll(shared, 0o750); err != nil {
		return "", fmt.Errorf("创建打码临时目录失败：%w", err)
	}
	base := newID()
	copied := filepath.Join(shared, base+".src.mp4")
	output := filepath.Join(shared, base+".out.mp4")
	if err := copyFile(copied, input); err != nil {
		return "", fmt.Errorf("复制待打码片段失败：%w", err)
	}
	defer os.Remove(copied)

	body, err := json.Marshal(faceGuardRequest{
		Input:    copied,
		Output:   output,
		Mode:     setting.Mode,
		MaxFaces: setting.MaxFaces,
		Sample:   setting.Sample,
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, faceGuardRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, setting.URL+"/redact", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: faceGuardRequestTimeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("人脸打码服务不可达：%w", err)
	}
	defer resp.Body.Close()
	var result faceGuardResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("人脸打码响应无法解析：%w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return "", fmt.Errorf("人脸打码失败：%s", result.Error)
	}
	if _, err := os.Stat(output); err != nil {
		return "", fmt.Errorf("人脸打码产物缺失：%w", err)
	}
	return output, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
