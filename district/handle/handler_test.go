package handle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

// R5-P2-08: parentAdCode=0 现在返回错误（必填参数）
func TestGetChildren_EmptyParentAdCode(t *testing.T) {
	svc := &DistrictServiceImpl{
		store: nil, // not used in this path
	}

	_, err := svc.GetChildren(context.Background(), &v1.GetChildrenReq{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parentAdCode is required")
}

// R5-P2-08: nil request (GetParentAdCode returns 0) 也返回错误
func TestGetChildren_NilRequest(t *testing.T) {
	svc := &DistrictServiceImpl{
		store: nil,
	}

	_, err := svc.GetChildren(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parentAdCode is required")
}

// R7-P2-10: NewDistrictHandler 拒绝 nil store
func TestNewDistrictHandler_NilStore(t *testing.T) {
	_, err := NewDistrictHandler(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "store must not be nil")
}

// R7-P2-10: NewDistrictHandler 正常创建
func TestNewDistrictHandler_ValidStore(t *testing.T) {
	// mockStore 满足 DistrictStore 接口即可，这里不需要功能测试
	_, err := NewDistrictHandler(&mockStoreForTest{})
	assert.NoError(t, err)
}

// mockStoreForTest 是一个最小的 DistrictStore mock，仅用于编译验证
type mockStoreForTest struct{}

func (m *mockStoreForTest) GetProvinces(ctx context.Context) ([]*v1.DistrictNode, error) {
	return nil, nil
}

func (m *mockStoreForTest) GetChildrenByParent(ctx context.Context, code uint32) ([]*v1.DistrictNode, error) {
	return nil, nil
}

func (m *mockStoreForTest) UpdateDistrict(ctx context.Context) error {
	return nil
}
