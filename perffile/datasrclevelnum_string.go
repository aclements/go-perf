// Code generated by "stringer -type=DataSrcLevelNum"; DO NOT EDIT.

package perffile

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[DataSrcLevelNumL1-1]
	_ = x[DataSrcLevelNumL2-2]
	_ = x[DataSrcLevelNumL3-3]
	_ = x[DataSrcLevelNumL4-4]
	_ = x[DataSrcLevelNumAnyCache-11]
	_ = x[DataSrcLevelNumLFB-12]
	_ = x[DataSrcLevelNumRAM-13]
	_ = x[DataSrcLevelNumPMEM-14]
	_ = x[DataSrcLevelNumNA-15]
}

const (
	_DataSrcLevelNum_name_0 = "DataSrcLevelNumL1DataSrcLevelNumL2DataSrcLevelNumL3DataSrcLevelNumL4"
	_DataSrcLevelNum_name_1 = "DataSrcLevelNumAnyCacheDataSrcLevelNumLFBDataSrcLevelNumRAMDataSrcLevelNumPMEMDataSrcLevelNumNA"
)

var (
	_DataSrcLevelNum_index_0 = [...]uint8{0, 17, 34, 51, 68}
	_DataSrcLevelNum_index_1 = [...]uint8{0, 23, 41, 59, 78, 95}
)

func (i DataSrcLevelNum) String() string {
	switch {
	case 1 <= i && i <= 4:
		i -= 1
		return _DataSrcLevelNum_name_0[_DataSrcLevelNum_index_0[i]:_DataSrcLevelNum_index_0[i+1]]
	case 11 <= i && i <= 15:
		i -= 11
		return _DataSrcLevelNum_name_1[_DataSrcLevelNum_index_1[i]:_DataSrcLevelNum_index_1[i+1]]
	default:
		return "DataSrcLevelNum(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
