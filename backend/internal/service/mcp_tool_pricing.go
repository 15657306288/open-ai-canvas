package service

import (
	"encoding/json"
	"errors"
	"strings"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// MCP 网关工具调用定价表（后端定价，连接器不再本地定价）。
//
// 设计原则：定价的唯一事实来源是影策后端画布（system_setting 持久化），
// 网关在 reserve 时传 amountMicrocredits=0，由本模块按工具名计算并冻结金额。
// 支持精确匹配与前缀通配（canvas_* 等），与网关旧本地 pricing 文件能力对齐。
const mcpToolPricingKey = "mcp_tool_pricing"

// MCPToolPricing 是 MCP 工具调用单价表（单位：microcredits/次）。
type MCPToolPricing struct {
	// DefaultMicrocredits 未命中任何规则时的默认单价（正整数 microcredits）。
	DefaultMicrocredits int64 `json:"defaultMicrocredits"`
	// ByTool 精确工具名或前缀通配（键以 * 结尾）→ 单价。
	ByTool map[string]int64 `json:"byTool"`
}

func defaultMCPToolPricing() MCPToolPricing {
	return MCPToolPricing{
		// 与网关旧 gateway-pricing.json 的默认值对齐（0.01 积分/次），避免上线后价格跳变。
		DefaultMicrocredits: 10_000,
		ByTool: map[string]int64{
			"canvas_get_context": 20_000,
			"video_generation":   1_500_000,
			"canvas_*":           20_000,
		},
	}
}

func validateMCPToolPricing(p MCPToolPricing) error {
	if p.DefaultMicrocredits <= 0 || p.DefaultMicrocredits > 1_000_000*CreditScale {
		return BadAuthRequest("MCP 工具默认单价必须在 1 microcredits 到 1000000 积分之间")
	}
	for tool, price := range p.ByTool {
		tool = strings.TrimSpace(tool)
		if tool == "" || len(tool) > internalMaxToolLength || price <= 0 || price > 1_000_000*CreditScale {
			return BadAuthRequest("MCP 工具定价配置无效")
		}
	}
	return nil
}

func (s *Service) mcpToolPricing() (MCPToolPricing, error) {
	setting, err := s.repo.SystemSetting(mcpToolPricingKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultMCPToolPricing(), nil
	}
	if err != nil {
		return MCPToolPricing{}, err
	}
	var pricing MCPToolPricing
	if err := json.Unmarshal([]byte(setting.ValueJSON), &pricing); err != nil {
		return MCPToolPricing{}, errors.New("MCP 工具定价配置格式无效")
	}
	if pricing.ByTool == nil {
		pricing.ByTool = map[string]int64{}
	}
	if err := validateMCPToolPricing(pricing); err != nil {
		return MCPToolPricing{}, err
	}
	return pricing, nil
}

// AdminMCPToolPricing 返回当前 MCP 工具定价（管理员可见）。
func (s *Service) AdminMCPToolPricing(actor *model.User) (MCPToolPricing, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return MCPToolPricing{}, err
	}
	return s.mcpToolPricing()
}

// UpdateMCPToolPricing 更新 MCP 工具定价表（管理员）。
func (s *Service) UpdateMCPToolPricing(actor *model.User, pricing MCPToolPricing) (MCPToolPricing, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return MCPToolPricing{}, err
	}
	if pricing.ByTool == nil {
		pricing.ByTool = map[string]int64{}
	}
	if err := validateMCPToolPricing(pricing); err != nil {
		return MCPToolPricing{}, err
	}
	encoded, err := json.Marshal(pricing)
	if err != nil {
		return MCPToolPricing{}, err
	}
	setting := model.SystemSetting{Key: mcpToolPricingKey, ValueJSON: string(encoded), UpdatedBy: actor.ID}
	current, err := s.repo.SystemSetting(mcpToolPricingKey)
	if err == nil {
		setting.CreatedAt = current.CreatedAt
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return MCPToolPricing{}, err
	}
	if err := s.repo.SaveSystemSetting(&setting); err != nil {
		return MCPToolPricing{}, err
	}
	if err := s.appendAdminAudit(actor, "mcp_tool_pricing.update", "system_setting", mcpToolPricingKey, "更新 MCP 工具定价", pricing); err != nil {
		return MCPToolPricing{}, err
	}
	return pricing, nil
}

// ToolPriceMicrocredits 计算某个 MCP 工具的单次调用定价（正整数 microcredits）。
// 匹配顺序：精确名 → 前缀通配（canvas_*）→ 默认单价。
func (s *Service) ToolPriceMicrocredits(tool string) (int64, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" || len(tool) > internalMaxToolLength {
		return 0, BadAuthRequest("tool 非法或过长")
	}
	pricing, err := s.mcpToolPricing()
	if err != nil {
		return 0, err
	}
	if exact, ok := pricing.ByTool[tool]; ok {
		return exact, nil
	}
	for pattern, price := range pricing.ByTool {
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(tool, strings.TrimSuffix(pattern, "*")) {
			return price, nil
		}
	}
	return pricing.DefaultMicrocredits, nil
}

// ModelPriceMicrocredits 按画布真实选择的模型（modelKey）返回单次调用价格。
//
// 定价真相是 channel_models（用户已按 a8api/实际成本配好的价格表）：
//   - 按次计费模型（image/video/audio 等，unit_price_microcredits>0）：返回真实单价。
//   - 文本 token 计价模型（unit_price=0 且 token 价>0）：固定金额结算无法按 token 精确扣费，
//     返回 (0, true, false)，由调用方决定按工具价兜底（P1 再做实际用量结算）。
//   - 查不到/未启用：返回 (0, false, false)。
func (s *Service) ModelPriceMicrocredits(modelKey string) (int64, bool, error) {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" || len(modelKey) > internalMaxToolLength {
		return 0, false, nil
	}
	cm, err := s.repo.ChannelModelByKeyAnyChannel(modelKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if cm.UnitPriceMicrocredits > 0 {
		return int64(cm.UnitPriceMicrocredits), true, nil
	}
	if cm.InputTokenPriceMicrocredits > 0 || cm.OutputTokenPriceMicrocredits > 0 || cm.CachedTokenPriceMicrocredits > 0 {
		// 文本 token 计价模型：固定结算下无法按 token 精确扣费，标记为"token 计价"。
		return 0, true, nil
	}
	return 0, false, nil
}
