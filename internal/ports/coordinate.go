package ports

import "github.com/Requim/AI-GDM/internal/domain/spatial"

// CoordinateTransformer 在地图供应商边界转换坐标参考系。
// 领域和数据库内部始终保存 WGS84，GCJ-02 只允许在适配器边界使用。
type CoordinateTransformer interface {
	// Convert 转换单个坐标，并返回是否实际发生转换及限制说明。
	Convert(point spatial.Point, from, to spatial.CoordinateSystem) (
		spatial.CoordinateConversion, error)
	// ConvertBatch 按输入顺序原子转换一批坐标，失败时不返回部分结果。
	ConvertBatch(points []spatial.Point, from, to spatial.CoordinateSystem) (
		[]spatial.CoordinateConversion, error)
}
