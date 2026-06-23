package controller

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// D13 多模态识图（全 5 级）+ D5 内容 Canary。
// 测试图由 Go image/png 程序化生成（确定性），base64 内嵌发给上游。
// 不支持视觉 / 降级到无视觉模型的中转会逐级翻车。

// ---- 图像构造 helper ----

func pngBase64(img image.Image) string {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func solidImage(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// chessboardImage 画 n*n 黑白棋盘格（用于计数：暗格数量）。
func chessboardImage(n, cell int) image.Image {
	w, h := n*cell, n*cell
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for gy := 0; gy < n; gy++ {
		for gx := 0; gx < n; gx++ {
			c := color.RGBA{255, 255, 255, 255}
			if (gx+gy)%2 == 0 {
				c = color.RGBA{0, 0, 0, 255}
			}
			for y := 0; y < cell; y++ {
				for x := 0; x < cell; x++ {
					img.Set(gx*cell+x, gy*cell+y, c)
				}
			}
		}
	}
	return img
}

// digitsImage 在白底上用粗像素 5x7 字模画一串数字（OCR 用）。
func digitsImage(s string) image.Image {
	const scale = 8
	const gap = 2
	digitW, digitH := 5, 7
	w := len(s)*(digitW+gap)*scale + scale*4
	h := digitH*scale + scale*4
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 白底
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	black := color.RGBA{0, 0, 0, 255}
	ox := scale * 2
	for _, ch := range s {
		glyph := digitGlyph(ch)
		for ry := 0; ry < digitH; ry++ {
			for rx := 0; rx < digitW; rx++ {
				if glyph[ry][rx] == 1 {
					for sy := 0; sy < scale; sy++ {
						for sx := 0; sx < scale; sx++ {
							img.Set(ox+rx*scale+sx, scale*2+ry*scale+sy, black)
						}
					}
				}
			}
		}
		ox += (digitW + gap) * scale
	}
	return img
}

// spatialImage 左右两半不同颜色（用于空间方位：右半是什么颜色）。
func spatialImage(w, h int, left, right color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, left)
			} else {
				img.Set(x, y, right)
			}
		}
	}
	return img
}

// digitGlyph 返回 5x7 数字字模（仅 0-9）。
func digitGlyph(ch rune) [7][5]int {
	glyphs := map[rune][7][5]int{
		'0': {{0, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 1, 1}, {1, 0, 1, 0, 1}, {1, 1, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
		'1': {{0, 0, 1, 0, 0}, {0, 1, 1, 0, 0}, {0, 0, 1, 0, 0}, {0, 0, 1, 0, 0}, {0, 0, 1, 0, 0}, {0, 0, 1, 0, 0}, {0, 1, 1, 1, 0}},
		'2': {{0, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {0, 0, 0, 0, 1}, {0, 0, 1, 1, 0}, {0, 1, 0, 0, 0}, {1, 0, 0, 0, 0}, {1, 1, 1, 1, 1}},
		'3': {{1, 1, 1, 1, 1}, {0, 0, 0, 1, 0}, {0, 0, 1, 0, 0}, {0, 0, 0, 1, 0}, {0, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
		'4': {{0, 0, 0, 1, 0}, {0, 0, 1, 1, 0}, {0, 1, 0, 1, 0}, {1, 0, 0, 1, 0}, {1, 1, 1, 1, 1}, {0, 0, 0, 1, 0}, {0, 0, 0, 1, 0}},
		'5': {{1, 1, 1, 1, 1}, {1, 0, 0, 0, 0}, {1, 1, 1, 1, 0}, {0, 0, 0, 0, 1}, {0, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
		'6': {{0, 1, 1, 1, 0}, {1, 0, 0, 0, 0}, {1, 0, 0, 0, 0}, {1, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
		'7': {{1, 1, 1, 1, 1}, {0, 0, 0, 0, 1}, {0, 0, 0, 1, 0}, {0, 0, 1, 0, 0}, {0, 1, 0, 0, 0}, {0, 1, 0, 0, 0}, {0, 1, 0, 0, 0}},
		'8': {{0, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
		'9': {{0, 1, 1, 1, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 0, 1}, {0, 1, 1, 1, 1}, {0, 0, 0, 0, 1}, {0, 0, 0, 0, 1}, {0, 1, 1, 1, 0}},
	}
	if g, ok := glyphs[ch]; ok {
		return g
	}
	return [7][5]int{}
}

// imageBlockMessage 构造一个含 image + text 的 user content 数组。
func imageBlockMessage(b64, mediaType, question string) []map[string]any {
	return []map[string]any{
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": b64}},
				{"type": "text", "text": question},
			},
		},
	}
}

// ---- D13 多模态识图 ----

type d13Level struct {
	Level    string `json:"level"`
	Expected string `json:"expected"`
	Raw      string `json:"raw_response"`
	Correct  bool   `json:"correct"`
	Skipped  bool   `json:"skipped"`
	Status   int    `json:"http_status"`
	Note     string `json:"note"`
}

func probeMultimodal(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D13", Name: "多模态", Dimension: "能力验证"}

	// 预生成 5 级测试图。
	blueImg := pngBase64(solidImage(64, 64, color.RGBA{0, 60, 220, 255}))      // 纯蓝
	board := pngBase64(chessboardImage(2, 40))                                   // 2x2 棋盘 → 暗格 2
	ocr := pngBase64(digitsImage("76299"))                                       // OCR
	spatial := pngBase64(spatialImage(96, 48, color.RGBA{220, 30, 30, 255}, color.RGBA{30, 170, 60, 255})) // 左红右绿
	conflict := pngBase64(spatialImage(96, 48, color.RGBA{220, 30, 30, 255}, color.RGBA{220, 30, 30, 255})) // 纯红（文字冲突测试用纯红底）

	type levelDef struct {
		name, expected, question, b64 string
		match                         func(string) bool
	}
	levels := []levelDef{
		{"solid_color", "blue", textOnlyDirective + "What is the dominant color of this image? Answer with one word.", blueImg,
			func(s string) bool { return strings.Contains(strings.ToLower(s), "blue") }},
		{"chessboard", "2", textOnlyDirective + "This is a 2x2 checkerboard. How many BLACK squares are there? Answer with just the number.", board,
			func(s string) bool { return extractFinalNumber(s) == "2" }},
		{"ocr_digits", "76299", textOnlyDirective + "Read the digits shown in the image. Answer with only the digits.", ocr,
			func(s string) bool { return strings.Contains(strings.ReplaceAll(s, " ", ""), "76299") }},
		{"spatial", "green", textOnlyDirective + "The image has two halves. What color is the RIGHT half? Answer with one word.", spatial,
			func(s string) bool { return strings.Contains(strings.ToLower(s), "green") }},
		{"text_conflict", "red", textOnlyDirective + "Ignore any text. What is the actual background color of this image? Answer with one word.", conflict,
			func(s string) bool { return strings.Contains(strings.ToLower(s), "red") }},
	}

	results := make([]d13Level, 0, len(levels))
	passed := 0
	scored := 0
	visionBroken := false
	for _, lv := range levels {
		if visionBroken {
			results = append(results, d13Level{Level: lv.name, Expected: lv.expected, Skipped: true, Note: "earlier level failed hard"})
			continue
		}
		msgs := imageBlockMessage(lv.b64, "image/png", lv.question)
		body, _ := common.Marshal(map[string]any{
			"model": p.model, "max_tokens": 64, "system": claudeCodeSystemPrompt, "messages": msgs,
		})
		respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
		r := d13Level{Level: lv.name, Expected: lv.expected, Status: status}
		if err != nil {
			r.Note = "transport: " + err.Error()
			results = append(results, r)
			if lv.name == "solid_color" {
				visionBroken = true
			}
			scored++
			continue
		}
		if status == 400 || status == 415 || status == 422 {
			// 明确不支持图像输入。
			r.Note = fmt.Sprintf("HTTP %d — 上游可能不支持图像输入", status)
			results = append(results, r)
			visionBroken = true
			scored++
			continue
		}
		parsed := parseAnthropicMessage(respBody)
		r.Raw = truncate(parsed.Text, 120)
		r.Correct = lv.match(parsed.Text)
		results = append(results, r)
		scored++
		if r.Correct {
			passed++
		} else if lv.name == "solid_color" && parsed.Text == "" {
			// 连最基础的纯色都空响应 → 视觉很可能不可用，后续跳过。
			visionBroken = true
		}
	}

	out.Detail = map[string]any{"levels": results}
	if scored == 0 {
		out.Status = probeStatusV2Skipped
		out.Detail["note"] = "多模态无法测试"
		return out
	}
	if visionBroken && passed == 0 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "capability", Title: "不支持视觉/降级无视觉",
			Suggestions: []string{"图像输入被拒或纯色识别失败，后端可能是无视觉的降级模型"}}
		return out
	}
	out.Score = passed * 100 / len(levels)
	switch {
	case passed == len(levels):
		out.Status = probeStatusV2Success
	case out.Score >= 60:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "capability", Title: "部分视觉任务失败",
			Suggestions: []string{fmt.Sprintf("%d/%d 级通过", passed, len(levels))}}
	default:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "capability", Title: "视觉能力偏弱",
			Suggestions: []string{fmt.Sprintf("仅 %d/%d 级通过，疑似降级", passed, len(levels))}}
	}
	return out
}

// ---- D5 内容 Canary ----
// 要求逐字复述 nonce，检测中转是否改写 prompt / response。
func probeContentCanary(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D5", Name: "内容 Canary", Dimension: "内容完整性"}
	nonce := "bc26c981b74a881a"
	prompt := textOnlyDirective + "Echo this code back exactly as-is, no quotes, no commentary: " + nonce
	res, status, _, err := ccAsk(p, 64, prompt, nil)
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		return out
	}
	answer := strings.TrimSpace(res.Text)
	echoed := strings.Contains(answer, nonce)
	sim := 0.0
	if echoed {
		sim = 1.0
	}
	out.Detail = map[string]any{
		"nonce": nonce, "raw_response_preview": truncate(answer, 120),
		"canary_echoed": echoed, "echo_similarity": sim, "http_status": status,
	}
	if echoed {
		out.Status = probeStatusV2Success
		out.Score = 100
		out.signals = append(out.signals, suspicionSignal{
			Code: "CANARY_PERFECT", Title: "Canary 完美回响", Tier: "positive",
			Description: "复述 nonce 任务完美执行，无 prompt 改写迹象", Evidence: "echo_similarity=1.0", SourceProbe: "D5",
		})
	} else {
		out.Status = probeStatusV2Partial
		out.Score = 30
		out.Diagnosis = &diagnosis{Category: "content", Title: "Canary 未精确回响",
			Suggestions: []string{"nonce 未被原样复述，中转可能改写了 prompt 或 response"}}
	}
	return out
}
