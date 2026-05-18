package handle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

// TestGetChildren_EmptyParentAdCode tests that GetChildren returns an empty list
// when the request has no parentAdCode, without hitting the database.
func TestGetChildren_EmptyParentAdCode(t *testing.T) {
	svc := &DistrictServiceImpl{
		districtDal: nil, // not used in this path
	}

	resp, err := svc.GetChildren(context.Background(), &v1.GetChildrenReq{
		ParentAdCode: "",
	})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.GetList())
}

// TestGetChildren_NilRequest tests that GetChildren handles a nil request
// gracefully by returning an empty list (GetParentAdCode on nil returns "").
func TestGetChildren_NilRequest(t *testing.T) {
	svc := &DistrictServiceImpl{
		districtDal: nil,
	}

	resp, err := svc.GetChildren(context.Background(), nil)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.GetList())
}

// TestGetChildren_WithParentAdCode_NilDal tests that calling GetChildren with
// a non-empty parentAdCode panics when districtDal is nil, confirming the
// handler depends on the data layer.
func TestGetChildren_WithParentAdCode_NilDal(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r, "expected panic when districtDal is nil and parentAdCode is provided")
	}()

	svc := &DistrictServiceImpl{
		districtDal: nil,
	}

	_, _ = svc.GetChildren(context.Background(), &v1.GetChildrenReq{
		ParentAdCode: "110000",
	})
}

// TestGetProvinces_NilDal tests that GetProvinces panics when districtDal is
// nil, confirming the handler depends on the data layer.
func TestGetProvinces_NilDal(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r, "expected panic when districtDal is nil")
	}()

	svc := &DistrictServiceImpl{
		districtDal: nil,
	}

	_, _ = svc.GetProvinces(context.Background(), &v1.GetProvincesReq{})
}

// TestGetProvinces_NilRequest tests that GetProvinces handles a nil request
// gracefully by panicking due to nil districtDal (consistent behavior).
func TestGetProvinces_NilRequest(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r, "expected panic when districtDal is nil")
	}()

	svc := &DistrictServiceImpl{
		districtDal: nil,
	}

	_, _ = svc.GetProvinces(context.Background(), nil)
}
