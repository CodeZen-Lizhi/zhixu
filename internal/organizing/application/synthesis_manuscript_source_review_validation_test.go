package application

import "testing"

func TestSourceReviewBindsEveryObligationAndHonorsRejection(t *testing.T) {
	snapshot := SynthesisSourceReviewSnapshot{Targets: []SynthesisSourceReviewTarget{{Label: "N001", Paragraphs: []SynthesisSourceReviewParagraph{{Label: "N001/P00001"}, {Label: "N001/P00002"}}}, {Label: "N002", Paragraphs: []SynthesisSourceReviewParagraph{{Label: "N002/P00001"}}}}, Obligations: []SynthesisSourceReviewObligation{{Label: "O001", NoteLabel: "N001", SourceLabel: "S001"}, {Label: "O002", NoteLabel: "N002", SourceLabel: "S002"}}}
	cases := []struct {
		name, raw       string
		valid, accepted bool
	}{
		{"supported", `{"checks":[{"obligation":"O001","source":"S001","targets":["N001/P00002"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"},{"obligation":"O002","source":"S002","targets":["N002/P00001"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"}]}`, true, true},
		{"refused", `{"checks":[{"obligation":"O001","source":"S001","targets":[],"verdict":"UNSUPPORTED","reason_code":"CURRENT_TEXT_CONTRADICTS"},{"obligation":"O002","source":"S002","targets":[],"verdict":"UNCERTAIN","reason_code":"CONDITIONS_UNSUPPORTED"}]}`, true, false},
		{"missing", `{"checks":[{"obligation":"O001","source":"S001","targets":["N001/P00001"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"}]}`, false, false},
		{"cross target", `{"checks":[{"obligation":"O001","source":"S001","targets":["N002/P00001"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"},{"obligation":"O002","source":"S002","targets":[],"verdict":"UNCERTAIN","reason_code":"UNCERTAIN"}]}`, false, false},
		{"case variant", `{"Checks":[{"obligation":"O001","source":"S001","targets":[],"verdict":"UNCERTAIN","reason_code":"UNCERTAIN"}]}`, false, false},
		{"duplicate key", `{"checks":[],"checks":[]}`, false, false},
		{"unknown field", `{"checks":[],"accepted":true}`, false, false},
		{"unicode", `{"checks":[{"obligation":"\ud800","source":"S001","targets":[],"verdict":"UNCERTAIN","reason_code":"UNCERTAIN"}]}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, accepted, err := BindSourceReviewOutput([]byte(c.raw), snapshot)
			if (err == nil) != c.valid || accepted != c.accepted {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}
