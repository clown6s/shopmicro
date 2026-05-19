package data

import "testing"

func TestFlattenDistricts_Nested(t *testing.T) {
	city := amapDistrict{
		AdCode: "110100", Name: "市辖区", Level: "city",
		Center: "116.405285,39.904989",
		Districts: []amapDistrict{
			{AdCode: "110101", Name: "东城区", Level: "district", Center: "116.416357,39.928353"},
		},
	}
	province := amapDistrict{
		AdCode: "110000", Name: "北京市", Level: "province",
		Center:    "116.405285,39.904989",
		Districts: []amapDistrict{city},
	}
	input := []amapDistrict{
		{AdCode: "100000", Name: "中国", Level: "country", Districts: []amapDistrict{province}},
	}

	records := flattenDistricts(input, 0)

	for _, r := range records {
		if r.Name == "中国" {
			t.Error("country level should be skipped")
		}
	}

	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d: %+v", len(records), records)
	}

	if records[0].Name != "北京市" || records[0].ParentAdCode != 0 {
		t.Errorf("province record: %+v", records[0])
	}
	if records[1].Name != "市辖区" || records[1].ParentAdCode != 110000 {
		t.Errorf("city record: %+v", records[1])
	}
	if records[2].Name != "东城区" || records[2].ParentAdCode != 110100 {
		t.Errorf("district record: %+v", records[2])
	}
}

func TestFlattenDistricts_Empty(t *testing.T) {
	if n := len(flattenDistricts(nil, 0)); n != 0 {
		t.Errorf("nil input: got %d records", n)
	}
	if n := len(flattenDistricts([]amapDistrict{}, 0)); n != 0 {
		t.Errorf("empty input: got %d records", n)
	}
}

func TestFlattenDistricts_InvalidAdCode(t *testing.T) {
	input := []amapDistrict{
		{AdCode: "", Name: "空adcode", Level: "province", Center: "0,0"},
		{AdCode: "abc", Name: "非数字", Level: "province", Center: "0,0"},
		{AdCode: "0", Name: "零值", Level: "province", Center: "0,0"},
	}
	if n := len(flattenDistricts(input, 0)); n != 0 {
		t.Errorf("invalid adcodes should be skipped, got %d", n)
	}
}

func TestFlattenDistricts_LeafNodes(t *testing.T) {
	input := []amapDistrict{
		{AdCode: "440000", Name: "广东省", Level: "province", Center: "113.280637,23.125178"},
	}
	records := flattenDistricts(input, 0)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].AdCode != 440000 || records[0].ParentAdCode != 0 {
		t.Errorf("leaf province: %+v", records[0])
	}
}

func TestFlattenDistricts_ParentTracking(t *testing.T) {
	input := []amapDistrict{
		{
			AdCode: "330000", Name: "浙江省", Level: "province", Center: "120.153576,30.287459",
			Districts: []amapDistrict{
				{
					AdCode: "330100", Name: "杭州市", Level: "city", Center: "120.210792,30.246026",
					Districts: []amapDistrict{
						{AdCode: "330102", Name: "上城区", Level: "district", Center: "120.169297,30.242592"},
						{AdCode: "330106", Name: "西湖区", Level: "district", Center: "120.130182,30.259384"},
					},
				},
			},
		},
	}
	records := flattenDistricts(input, 0)
	if len(records) != 4 {
		t.Fatalf("expected 4 records, got %d", len(records))
	}
	// 验证 parentAdCode 链
	if records[1].ParentAdCode != 330000 {
		t.Errorf("city parentAdCode: got %d, want 330000", records[1].ParentAdCode)
	}
	if records[2].ParentAdCode != 330100 {
		t.Errorf("district1 parentAdCode: got %d, want 330100", records[2].ParentAdCode)
	}
	if records[3].ParentAdCode != 330100 {
		t.Errorf("district2 parentAdCode: got %d, want 330100", records[3].ParentAdCode)
	}
}
