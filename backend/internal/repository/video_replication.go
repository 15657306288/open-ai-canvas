package repository

import (
	"encoding/json"
	"errors"
	"maps"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

// 视频复刻的持久化：所有读取都带 user_id，跨用户读取等同于不存在。

func (r *Repository) CreateVideoReplicationProject(project *model.VideoReplicationProject) error {
	return r.db.Create(project).Error
}

func (r *Repository) VideoReplicationProject(userID, id string) (*model.VideoReplicationProject, error) {
	var project model.VideoReplicationProject
	if err := r.db.First(&project, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		return nil, err
	}
	return &project, nil
}

func (r *Repository) VideoReplicationProjects(userID string, limit int) ([]model.VideoReplicationProject, error) {
	var projects []model.VideoReplicationProject
	query := r.db.Select("id", "user_id", "title", "video_type", "status", "stage", "progress", "error", "created_at", "updated_at").
		Where("user_id = ?", userID)
	err := query.Order("updated_at DESC").Limit(limit).Find(&projects).Error
	return projects, err
}

// ActiveVideoReplicationProjects 返回 since 之后创建、仍在生成或合成中的项目（跨用户，供后台推进）。
func (r *Repository) ActiveVideoReplicationProjects(since time.Time, limit int) ([]model.VideoReplicationProject, error) {
	var projects []model.VideoReplicationProject
	err := r.db.Where("status IN ? AND created_at >= ?", []string{model.VideoReplicationStatusGenerating, model.VideoReplicationStatusComposing}, since).
		Order("updated_at ASC").Limit(limit).Find(&projects).Error
	return projects, err
}

func (r *Repository) UpdateVideoReplicationProject(userID, id string, values map[string]any) error {
	values["updated_at"] = time.Now()
	res := r.db.Model(&model.VideoReplicationProject{}).Where("id = ? AND user_id = ?", id, userID).Updates(values)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClaimVideoReplicationAnalysis 只在项目不处于分析中（或租约已过期）时把它切到 analyzing，
// 防止双击或多个后端进程重复分析同一项目。
func (r *Repository) ClaimVideoReplicationAnalysis(userID, id string, lease time.Time, startValues map[string]any) (bool, error) {
	// 租约由调用方以 UTC 保存；SQLite 按时间文本比较，比较值必须使用相同时区。
	now := time.Now().UTC()
	query := r.db.Model(&model.VideoReplicationProject{}).
		Where("id = ? AND user_id = ? AND (status <> ? OR analysis_lease_until IS NULL OR analysis_lease_until < ?)", id, userID, model.VideoReplicationStatusAnalyzing, now)
	values := map[string]any{"status": model.VideoReplicationStatusAnalyzing, "stage": "素材检查", "progress": 2, "error": "", "analysis_lease_until": lease, "updated_at": now}
	if startValues != nil {
		query = query.Where("status NOT IN ?", []string{model.VideoReplicationStatusGenerating, model.VideoReplicationStatusComposing})
		maps.Copy(values, startValues)
	}
	res := query.Updates(values)
	return res.RowsAffected == 1, res.Error
}

// UpdateVideoReplicationAnalysis 只在租约仍属于本次分析时写入，避免过期分析覆盖新一次的结果。
func (r *Repository) UpdateVideoReplicationAnalysis(userID, id string, lease time.Time, values map[string]any) (bool, error) {
	values["updated_at"] = time.Now()
	res := r.db.Model(&model.VideoReplicationProject{}).Where("id = ? AND user_id = ? AND status = ? AND analysis_lease_until = ?", id, userID, model.VideoReplicationStatusAnalyzing, lease).Updates(values)
	return res.RowsAffected == 1, res.Error
}

func (r *Repository) ExpireVideoReplicationAnalysis(userID, id string) error {
	now := time.Now().UTC()
	return r.db.Model(&model.VideoReplicationProject{}).
		Where("id = ? AND user_id = ? AND status = ? AND (analysis_lease_until IS NULL OR analysis_lease_until < ?)", id, userID, model.VideoReplicationStatusAnalyzing, now).
		Updates(map[string]any{"status": model.VideoReplicationStatusFailed, "stage": "分析中断", "error": "分析过程被中断（服务重启或超时），请重新分析", "analysis_lease_until": nil, "updated_at": now}).Error
}

// UpdateVideoReplicationStoryboard 以分镜修订号做乐观锁。
func (r *Repository) UpdateVideoReplicationStoryboard(userID, id string, revision int, storyboardJSON string) (bool, error) {
	res := r.db.Model(&model.VideoReplicationProject{}).
		Where("id = ? AND user_id = ? AND storyboard_revision = ? AND status <> ?", id, userID, revision, model.VideoReplicationStatusAnalyzing).
		Updates(map[string]any{"storyboard_json": storyboardJSON, "storyboard_revision": revision + 1, "updated_at": time.Now()})
	return res.RowsAffected == 1, res.Error
}

func (r *Repository) VideoReplicationAssets(userID, projectID string) ([]model.VideoReplicationAsset, error) {
	var assets []model.VideoReplicationAsset
	err := r.db.Where("project_id = ? AND user_id = ?", projectID, userID).Order("category ASC, sort_order ASC, created_at ASC").Find(&assets).Error
	return assets, err
}

func (r *Repository) CreateVideoReplicationAsset(asset *model.VideoReplicationAsset) error {
	return r.db.Create(asset).Error
}

func (r *Repository) DeleteVideoReplicationAsset(userID, projectID, id string) (*model.VideoReplicationAsset, error) {
	var asset model.VideoReplicationAsset
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&asset, "id = ? AND project_id = ? AND user_id = ?", id, projectID, userID).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.VideoReplicationAsset{}, "id = ?", asset.ID).Error; err != nil {
			return err
		}
		if !asset.IsPrimary {
			return nil
		}
		// 删除主素材后由同类第一个素材接任，保证每类最多一个且有素材时总有主素材。
		var next model.VideoReplicationAsset
		err := tx.Where("project_id = ? AND user_id = ? AND category = ?", projectID, userID, asset.Category).Order("sort_order ASC, created_at ASC").First(&next).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.Model(&model.VideoReplicationAsset{}).Where("id = ?", next.ID).Update("is_primary", true).Error
	})
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// ReorderVideoReplicationAssets 按传入顺序重写同类素材的 sort_order；primaryID 非空时同时切换主素材。
func (r *Repository) ReorderVideoReplicationAssets(userID, projectID, category string, ids []string, primaryID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing []model.VideoReplicationAsset
		if err := tx.Where("project_id = ? AND user_id = ? AND category = ?", projectID, userID, category).Find(&existing).Error; err != nil {
			return err
		}
		known := map[string]bool{}
		for _, asset := range existing {
			known[asset.ID] = true
		}
		if len(ids) != len(existing) {
			return errors.New("素材顺序与现有素材不一致")
		}
		for index, id := range ids {
			if !known[id] {
				return errors.New("素材顺序与现有素材不一致")
			}
			updates := map[string]any{"sort_order": index, "updated_at": time.Now()}
			if primaryID != "" {
				updates["is_primary"] = id == primaryID
			}
			if err := tx.Model(&model.VideoReplicationAsset{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) UpdateVideoReplicationAssetProfile(id, profileJSON string) error {
	return r.db.Model(&model.VideoReplicationAsset{}).Where("id = ?", id).Updates(map[string]any{"profile_json": profileJSON, "updated_at": time.Now()}).Error
}

func (r *Repository) VideoReplicationRuns(userID, projectID string) ([]model.VideoReplicationShotRun, error) {
	var runs []model.VideoReplicationShotRun
	err := r.db.Where("project_id = ? AND user_id = ?", projectID, userID).Order("shot_id ASC, version ASC").Find(&runs).Error
	return runs, err
}

// VideoReplicationRunByTaskID 按生成任务 ID 找复刻镜头版本（后台请求明细标注参考素材用途）。
func (r *Repository) VideoReplicationRunByTaskID(taskID string) (*model.VideoReplicationShotRun, error) {
	var run model.VideoReplicationShotRun
	if err := r.db.Where("task_id = ?", taskID).Order("created_at DESC").First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *Repository) VideoReplicationRunByKey(projectID, key string) (*model.VideoReplicationShotRun, error) {
	var run model.VideoReplicationShotRun
	if err := r.db.First(&run, "project_id = ? AND idempotency_key = ?", projectID, key).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// ReserveVideoReplicationRun 在同一事务里确认该分镜没有进行中的版本，再插入新版本。
// 唯一索引 (project_id, idempotency_key) 挡住同一键的并发重复提交。
func (r *Repository) ReserveVideoReplicationRun(run *model.VideoReplicationShotRun) (bool, error) {
	if run.Kind == "" {
		run.Kind = "video"
	}
	created := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&model.VideoReplicationShotRun{}).Where("project_id = ? AND shot_id = ? AND kind = ? AND status IN ?", run.ProjectID, run.ShotID, run.Kind, []string{model.VideoReplicationRunSubmitting, model.VideoReplicationRunQueued, model.VideoReplicationRunRunning}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return nil
		}
		var latest struct{ Version int }
		if err := tx.Model(&model.VideoReplicationShotRun{}).Select("COALESCE(MAX(version), 0) AS version").Where("project_id = ? AND shot_id = ? AND kind = ?", run.ProjectID, run.ShotID, run.Kind).Scan(&latest).Error; err != nil {
			return err
		}
		run.Version = latest.Version + 1
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

func (r *Repository) UpdateVideoReplicationRun(id string, values map[string]any) error {
	values["updated_at"] = time.Now()
	return r.db.Model(&model.VideoReplicationShotRun{}).Where("id = ?", id).Updates(values).Error
}

// SelectVideoReplicationRun 把指定版本设为该分镜的采用版本（每个分镜只有一个）。
// ClearVideoReplicationRunSelection 取消项目所有镜头版本的「采用」，之后按最新成功版本采用（整体重新生成时用）。
func (r *Repository) ClearVideoReplicationRunSelection(userID, projectID string) error {
	return r.db.Model(&model.VideoReplicationShotRun{}).Where("project_id = ? AND user_id = ?", projectID, userID).Update("selected", false).Error
}

func (r *Repository) SelectVideoReplicationRun(userID, projectID, runID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var run model.VideoReplicationShotRun
		if err := tx.First(&run, "id = ? AND project_id = ? AND user_id = ?", runID, projectID, userID).Error; err != nil {
			return err
		}
		if run.Status != model.VideoReplicationRunSucceeded || run.OutputResourceID == "" {
			return errors.New("只能采用已成功生成的镜头版本")
		}
		if err := tx.Model(&model.VideoReplicationShotRun{}).Where("project_id = ? AND shot_id = ? AND kind = ?", projectID, run.ShotID, run.Kind).Update("selected", false).Error; err != nil {
			return err
		}
		return tx.Model(&model.VideoReplicationShotRun{}).Where("id = ?", run.ID).Update("selected", true).Error
	})
}

func (r *Repository) TaskForUserByRequestID(userID, requestID string) (*model.Task, error) {
	var task model.Task
	if err := r.db.Where("user_id = ? AND request_id = ?", userID, requestID).Order("created_at DESC").First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *Repository) VideoReplicationOutputs(userID, projectID string) ([]model.VideoReplicationOutput, error) {
	var outputs []model.VideoReplicationOutput
	err := r.db.Where("project_id = ? AND user_id = ?", projectID, userID).Order("created_at DESC").Find(&outputs).Error
	return outputs, err
}

func (r *Repository) CreateVideoReplicationOutput(output *model.VideoReplicationOutput) error {
	return r.db.Create(output).Error
}

func (r *Repository) VideoReplicationOutputByKey(projectID, key string) (*model.VideoReplicationOutput, error) {
	var output model.VideoReplicationOutput
	if err := r.db.First(&output, "project_id = ? AND idempotency_key = ?", projectID, key).Error; err != nil {
		return nil, err
	}
	return &output, nil
}

func (r *Repository) UpdateVideoReplicationOutput(id string, values map[string]any) error {
	values["updated_at"] = time.Now()
	return r.db.Model(&model.VideoReplicationOutput{}).Where("id = ?", id).Updates(values).Error
}

// ClaimVideoReplicationOutputCheck 把待检查的成片切到 inspecting；检查进程中断超过 10 分钟后允许重新领取。
func (r *Repository) ClaimVideoReplicationOutputCheck(id string) (bool, error) {
	now := time.Now()
	res := r.db.Model(&model.VideoReplicationOutput{}).
		Where("id = ? AND (status = ? OR (status = ? AND updated_at < ?))", id, "checking", "inspecting", now.Add(-10*time.Minute)).
		Updates(map[string]any{"status": "inspecting", "updated_at": now})
	return res.RowsAffected == 1, res.Error
}

// VideoReplicationKeyUsed 判断某个提交标识是否已产生过镜头版本；重放的请求据此直接返回，不再继续提交。
func (r *Repository) VideoReplicationKeyUsed(projectID, key string) (bool, error) {
	var count int64
	prefix := key + ":"
	// substr 在 SQLite 与 PostgreSQL 中语义一致，避免 LIKE 对 _ 的转义差异。
	err := r.db.Model(&model.VideoReplicationShotRun{}).Where("project_id = ? AND substr(idempotency_key, 1, ?) = ?", projectID, len(prefix), prefix).Count(&count).Error
	return count > 0, err
}

// VideoReplicationAdminProject 是后台监控列表的一行。
type VideoReplicationAdminProject struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	Title       string    `json:"title"`
	VideoType   string    `json:"videoType"`
	Status      string    `json:"status"`
	Stage       string    `json:"stage"`
	Error       string    `json:"error"`
	Paused      bool      `json:"paused"`
	PlanJSON    string    `json:"-"`
	ParamsJSON  string    `json:"-" gorm:"column:parameters_json"`
	ReplaceMode string    `json:"replaceMode" gorm:"-"`
	AudioMode   string    `json:"audioMode" gorm:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	Runs        int64     `json:"runs"`
	FailedRuns  int64     `json:"failedRuns"`
	ActiveRuns  int64     `json:"activeRuns"`
	Outputs     int64     `json:"outputs"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Strategy    string    `json:"strategy"`
}

func (r *Repository) AdminVideoReplicationProjects(limit int) ([]VideoReplicationAdminProject, error) {
	var rows []VideoReplicationAdminProject
	err := r.db.Table("video_replication_projects AS p").
		Select(`p.id, p.user_id, u.username, u.display_name, p.title, p.video_type, p.status, p.stage, p.error, p.paused, p.plan_json, p.parameters_json, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM video_replication_shot_runs r WHERE r.project_id = p.id) AS runs,
			(SELECT COUNT(*) FROM video_replication_shot_runs r WHERE r.project_id = p.id AND r.status IN ('failed','cancelled')) AS failed_runs,
			(SELECT COUNT(*) FROM video_replication_shot_runs r WHERE r.project_id = p.id AND r.status IN ('submitting','queued','running')) AS active_runs,
			(SELECT COUNT(*) FROM video_replication_outputs o WHERE o.project_id = p.id) AS outputs`).
		Joins("LEFT JOIN users u ON u.id = p.user_id").
		Order("p.updated_at DESC").Limit(limit).Scan(&rows).Error
	for index := range rows {
		var plan struct {
			Strategy string `json:"strategy"`
		}
		if json.Unmarshal([]byte(rows[index].PlanJSON), &plan) == nil {
			rows[index].Strategy = plan.Strategy
		}
		var params struct {
			ReplaceMode string `json:"replaceMode"`
			AudioMode   string `json:"audioMode"`
		}
		if json.Unmarshal([]byte(rows[index].ParamsJSON), &params) == nil {
			rows[index].ReplaceMode, rows[index].AudioMode = params.ReplaceMode, params.AudioMode
		}
	}
	return rows, err
}

// AccumulateVideoReplicationCostUsage 把一次上游请求的用量累加到进行中的分析成本账单上（只改 reserved 状态的账单）。
func (r *Repository) AccumulateVideoReplicationCostUsage(orderID string, input, output, cached int64, usage bool, source, providerRequestID string) error {
	updates := map[string]any{
		"input_tokens": gorm.Expr("input_tokens + ?", input), "output_tokens": gorm.Expr("output_tokens + ?", output), "cached_tokens": gorm.Expr("cached_tokens + ?", cached),
		"updated_at": time.Now(),
	}
	if usage {
		updates["usage_available"] = true
	}
	// 「上游返回了用量」优先于「成功但没有用量」，不能被后一次请求降级。
	updates["usage_source"] = gorm.Expr("CASE WHEN usage_source = ? THEN usage_source ELSE ? END", "provider", source)
	if providerRequestID != "" {
		updates["provider_request_id"] = providerRequestID
	}
	return r.db.Model(&model.BillingOrder{}).Where("id = ? AND status = ?", orderID, model.BillingStatusReserved).Updates(updates).Error
}

// TransitionVideoReplicationCostOrder 只在账单仍处于 from 状态时更新，返回是否命中；重复结算因此只生效一次。
func (r *Repository) TransitionVideoReplicationCostOrder(orderID string, from model.BillingStatus, updates map[string]any) (bool, error) {
	result := r.db.Model(&model.BillingOrder{}).Where("id = ? AND status = ?", orderID, from).Updates(updates)
	return result.RowsAffected == 1, result.Error
}

// StaleVideoReplicationCostOrders 返回早于 before 仍在进行中的平台承担成本账单（调用中途进程退出留下的）。
func (r *Repository) StaleVideoReplicationCostOrders(scene string, before time.Time, limit int) ([]model.BillingOrder, error) {
	var orders []model.BillingOrder
	err := r.db.Where("scene = ? AND status = ? AND created_at < ?", scene, model.BillingStatusReserved, before).Order("created_at ASC").Limit(limit).Find(&orders).Error
	return orders, err
}

// DeleteVideoReplicationProject 删除项目及其素材登记、镜头版本与成片版本；资源本身留在素材库，已提交的任务记录不受影响。
func (r *Repository) DeleteVideoReplicationProject(userID, id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND user_id = ? AND status NOT IN ?", id, userID, []string{
			model.VideoReplicationStatusAnalyzing,
			model.VideoReplicationStatusGenerating,
			model.VideoReplicationStatusComposing,
		}).Delete(&model.VideoReplicationProject{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		for _, item := range []any{&model.VideoReplicationAsset{}, &model.VideoReplicationShotRun{}, &model.VideoReplicationOutput{}} {
			if err := tx.Where("project_id = ? AND user_id = ?", id, userID).Delete(item).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// VideoReplicationOverview 是后台「运行总览」的真实统计（直接来自复刻项目、镜头任务与成片表）。
type VideoReplicationOverview struct {
	Projects        int64            `json:"projects"`
	ProjectsInRange int64            `json:"projectsInRange"`
	Users           int64            `json:"users"`
	ByStatus        map[string]int64 `json:"byStatus"`
	Runs            map[string]int64 `json:"runs"`
	Outputs         int64            `json:"outputs"`
	ByMode          map[string]int64 `json:"byMode"`
	RecentErrors    []struct {
		Error string `json:"error"`
		Count int64  `json:"count"`
	} `json:"recentErrors"`
}

func (r *Repository) VideoReplicationOverview(since time.Time) (*VideoReplicationOverview, error) {
	out := &VideoReplicationOverview{ByStatus: map[string]int64{}, Runs: map[string]int64{}, ByMode: map[string]int64{}}
	if err := r.db.Table("video_replication_projects").Where("status <> ?", model.VideoReplicationStatusDraft).Count(&out.Projects).Error; err != nil {
		return nil, err
	}
	scope := r.db.Table("video_replication_projects").Where("status <> ? AND created_at >= ?", model.VideoReplicationStatusDraft, since)
	if err := scope.Count(&out.ProjectsInRange).Error; err != nil {
		return nil, err
	}
	if err := r.db.Table("video_replication_projects").Where("status <> ? AND created_at >= ?", model.VideoReplicationStatusDraft, since).Distinct("user_id").Count(&out.Users).Error; err != nil {
		return nil, err
	}
	var grouped []struct {
		Key   string
		Count int64
	}
	if err := r.db.Table("video_replication_projects").Select("status AS key, COUNT(*) AS count").Where("status <> ? AND created_at >= ?", model.VideoReplicationStatusDraft, since).Group("status").Scan(&grouped).Error; err != nil {
		return nil, err
	}
	for _, row := range grouped {
		out.ByStatus[row.Key] = row.Count
	}
	grouped = nil
	if err := r.db.Table("video_replication_shot_runs").Select("status AS key, COUNT(*) AS count").Where("created_at >= ?", since).Group("status").Scan(&grouped).Error; err != nil {
		return nil, err
	}
	for _, row := range grouped {
		out.Runs[row.Key] = row.Count
	}
	if err := r.db.Table("video_replication_outputs").Where("status = ? AND created_at >= ?", "succeeded", since).Count(&out.Outputs).Error; err != nil {
		return nil, err
	}
	var params []string
	if err := r.db.Table("video_replication_projects").Where("status <> ? AND created_at >= ?", model.VideoReplicationStatusDraft, since).Order("created_at DESC").Limit(5000).Pluck("parameters_json", &params).Error; err != nil {
		return nil, err
	}
	for _, raw := range params {
		var value struct {
			ReplaceMode string `json:"replaceMode"`
		}
		_ = json.Unmarshal([]byte(raw), &value)
		if value.ReplaceMode == "" {
			value.ReplaceMode = "legacy"
		}
		out.ByMode[value.ReplaceMode]++
	}
	if err := r.db.Table("video_replication_projects").Select("error, COUNT(*) AS count").Where("status = ? AND error <> '' AND created_at >= ?", model.VideoReplicationStatusFailed, since).Group("error").Order("count DESC").Limit(6).Scan(&out.RecentErrors).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// VideoReplicationProjectOwner 返回项目所属用户（后台查看任意用户的项目详情用）。
func (r *Repository) VideoReplicationProjectOwner(id string) (string, error) {
	var project model.VideoReplicationProject
	if err := r.db.Select("id", "user_id").First(&project, "id = ?", id).Error; err != nil {
		return "", err
	}
	return project.UserID, nil
}

// EnabledSystemChannelModelsByIDs 读取仍然启用的系统渠道模型（渠道与模型都启用、未删除），供商品视频后台校验所选模型。
func (r *Repository) EnabledSystemChannelModelsByIDs(ids []string) ([]model.ChannelModel, error) {
	var items []model.ChannelModel
	if len(ids) == 0 {
		return items, nil
	}
	err := r.db.Model(&model.ChannelModel{}).
		Select("channel_models.id, channel_models.channel_id, channel_models.model_key, channel_models.capability").
		Joins("JOIN model_channels ON model_channels.id = channel_models.channel_id").
		Where("channel_models.id IN ? AND channel_models.enabled = ? AND model_channels.enabled = ? AND model_channels.scope = ? AND model_channels.deleted_at IS NULL", uniqueStrings(ids), true, true, model.ChannelScopeSystem).
		Find(&items).Error
	return items, err
}
