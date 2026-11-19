package workbench

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/model"
)

func TestEnsureBuiltinCatalogPublishesImageAndVideoPresets(t *testing.T) {
	svc, db, _ := newWorkbenchTest(t)
	if err := svc.EnsureBuiltinCatalog(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{KindImage, KindVideo} {
		tools, err := svc.PublicTools(kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) == 0 {
			t.Fatalf("no builtin %s tools", kind)
		}
		for _, tool := range tools {
			if len(tool.Presets) != 3 {
				t.Fatalf("tool %s has %d public presets, want 3", tool.ID, len(tool.Presets))
			}
			for _, preset := range tool.Presets {
				if preset.ToolID != tool.ID || preset.PromptTemplate == "" || !preset.Enabled {
					t.Fatalf("unusable public preset: %#v", preset)
				}
			}
		}
	}
	var count int64
	if err := db.Model(&model.WorkbenchTool{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Fatalf("tool count=%d", count)
	}
}

func TestEnsureBuiltinCatalogIsRepeatableAndPreservesAdminState(t *testing.T) {
	svc, db, _ := newWorkbenchTest(t)
	seedTools, seedPresets, err := builtinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	editedTool := seedTools[0]
	editedTool.Name = "管理员自定义名称"
	editedTool.Enabled = false
	editedTool.Version = 9
	editedPreset := seedPresets[0]
	editedPreset.Name = "管理员自定义预设"
	editedPreset.PromptTemplate = "管理员自己的提示词"
	editedPreset.Enabled = false
	editedPreset.Version = 6
	if err := db.Create(&editedTool).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&editedPreset).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBuiltinCatalog(); err != nil {
		t.Fatal(err)
	}
	var firstTools, firstPresets int64
	if err := db.Model(&model.WorkbenchTool{}).Count(&firstTools).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WorkbenchPreset{}).Count(&firstPresets).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBuiltinCatalog(); err != nil {
		t.Fatal(err)
	}
	var secondTools, secondPresets int64
	if err := db.Model(&model.WorkbenchTool{}).Count(&secondTools).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WorkbenchPreset{}).Count(&secondPresets).Error; err != nil {
		t.Fatal(err)
	}
	if firstTools != secondTools || firstPresets != secondPresets {
		t.Fatalf("repeat seed changed counts: tools %d/%d presets %d/%d", firstTools, secondTools, firstPresets, secondPresets)
	}
	var gotTool model.WorkbenchTool
	if err := db.First(&gotTool, "id = ?", editedTool.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotTool.Name != editedTool.Name || gotTool.Enabled != editedTool.Enabled || gotTool.Version != editedTool.Version {
		t.Fatalf("admin tool changed: %#v", gotTool)
	}
	var gotPreset model.WorkbenchPreset
	if err := db.First(&gotPreset, "id = ?", editedPreset.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotPreset.Name != editedPreset.Name || gotPreset.PromptTemplate != editedPreset.PromptTemplate || gotPreset.Enabled != editedPreset.Enabled || gotPreset.Version != editedPreset.Version {
		t.Fatalf("admin preset changed: %#v", gotPreset)
	}
}

func TestBuiltinCatalogVideoCreationToolsDeclareSlots(t *testing.T) {
	tools, _, err := builtinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.WorkbenchTool{}
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	slotsOf := func(key string) []ReferenceSlot {
		t.Helper()
		tool, ok := byID[builtinCatalogID("tool/"+key)]
		if !ok || tool.Kind != KindVideo {
			t.Fatalf("missing builtin video tool %s", key)
		}
		var capability struct {
			Recommended   bool          `json:"recommended"`
			InputContract InputContract `json:"inputContract"`
		}
		if err := json.Unmarshal([]byte(tool.CapabilityJSON), &capability); err != nil {
			t.Fatal(err)
		}
		if key != "camera-motion-study" && !capability.Recommended {
			t.Fatalf("%s must be listed under recommended", key)
		}
		if !capability.InputContract.PromptRequired {
			t.Fatalf("%s must require a prompt", key)
		}
		return capability.InputContract.Slots
	}

	remix := slotsOf("script-framework-remix")
	if len(remix) != 2 {
		t.Fatalf("remix slots = %#v", remix)
	}
	if source := remix[0]; !source.Required || source.MinCount != 1 || source.MaxCount != 1 || len(source.MediaTypes) != 1 || source.MediaTypes[0] != "video" {
		t.Fatalf("remix source slot = %#v", source)
	}
	if product := remix[1]; !product.Required || product.MinCount != 1 || product.MaxCount != 4 || len(product.MediaTypes) != 1 || product.MediaTypes[0] != "image" {
		t.Fatalf("remix product slot = %#v", product)
	}

	replace := slotsOf("subject-replacement-remix")
	if len(replace) != 2 {
		t.Fatalf("replacement slots = %#v", replace)
	}
	if source := replace[0]; !source.Required || source.MinCount != 1 || source.MaxCount != 1 || len(source.MediaTypes) != 1 || source.MediaTypes[0] != "video" {
		t.Fatalf("replacement source slot = %#v", source)
	}
	if material := replace[1]; !material.Required || material.MinCount != 1 || material.MaxCount != 4 || len(material.MediaTypes) != 1 || material.MediaTypes[0] != "image" {
		t.Fatalf("replacement material slot = %#v", material)
	}

	prompt := slotsOf("prompt-video")
	if len(prompt) != 1 || prompt[0].Required || prompt[0].MinCount != 0 || prompt[0].MaxCount != 9 || len(prompt[0].MediaTypes) != 3 {
		t.Fatalf("prompt video slots = %#v", prompt)
	}

	director := slotsOf("director-creation")
	if len(director) != 2 || !director[0].Required || director[0].MaxCount != 9 || director[1].Required || director[1].MaxCount != 8 {
		t.Fatalf("director slots = %#v", director)
	}

	// Legacy entries keep their original contract: required single-type main slot, optional support slot, max 4.
	legacy := slotsOf("camera-motion-study")
	if len(legacy) != 2 || !legacy[0].Required || legacy[0].MinCount != 1 || legacy[0].MaxCount != 4 || legacy[1].Required || legacy[1].MinCount != 0 || legacy[1].MaxCount != 4 {
		t.Fatalf("legacy slots changed: %#v", legacy)
	}
}

func TestBuiltinCatalogImageStudioToolsDeclareSlots(t *testing.T) {
	tools, _, err := builtinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.WorkbenchTool{}
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	expected := map[string][2]int{
		"ai-main-image":      {9, 4},
		"ai-sku-image":       {9, 4},
		"ai-detail-page":     {9, 4},
		"ai-image-edit":      {1, 2},
		"ai-product-suite":   {9, 4},
		"ai-product-fission": {1, 4},
		"ai-interior-render": {4, 4},
		"ai-interior-tools":  {4, 4},
	}
	for key, maxCounts := range expected {
		tool, ok := byID[builtinCatalogID("tool/"+key)]
		if !ok || tool.Kind != KindImage {
			t.Fatalf("missing builtin image tool %s", key)
		}
		var capability struct {
			Recommended   bool          `json:"recommended"`
			InputContract InputContract `json:"inputContract"`
		}
		if err := json.Unmarshal([]byte(tool.CapabilityJSON), &capability); err != nil {
			t.Fatal(err)
		}
		if !capability.Recommended {
			t.Fatalf("%s must be listed under recommended", key)
		}
		if !capability.InputContract.PromptRequired {
			t.Fatalf("%s must require a prompt", key)
		}
		slots := capability.InputContract.Slots
		if len(slots) != 2 {
			t.Fatalf("%s slots = %#v", key, slots)
		}
		if main := slots[0]; !main.Required || main.MinCount != 1 || main.MaxCount != maxCounts[0] || len(main.MediaTypes) != 1 || main.MediaTypes[0] != "image" {
			t.Fatalf("%s main slot = %#v", key, main)
		}
		if support := slots[1]; support.Required || support.MinCount != 0 || support.MaxCount != maxCounts[1] || len(support.MediaTypes) != 1 || support.MediaTypes[0] != "image" {
			t.Fatalf("%s support slot = %#v", key, support)
		}
	}
}
