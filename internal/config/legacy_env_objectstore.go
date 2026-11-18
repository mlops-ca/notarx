package config

// Object store variables removed by ADR 0017. The GATEWAY_ names went through
// the ADR 0013 rename first and the NOTARX_RUSTFS_ names are their interim
// form; neither is read. They are refused at startup with the final name
// (ADR 0013: no aliases, no fallback reads).
const (
	legacyGatewayPrefix = "GATEWAY" + "_"
	legacyRustFSPrefix  = "RUSTFS" + "_"
)

var renamedObjectStoreEnv = func() map[string]string {
	renames := map[string]string{
		"ENDPOINT":   envObjectStoreEndpoint,
		"BUCKET":     envObjectStoreBucket,
		"ACCESS_KEY": envObjectStoreAccessKey,
		"SECRET_KEY": envObjectStoreSecretKey,
	}
	result := make(map[string]string, 2*len(renames))
	for suffix, replacement := range renames {
		result[legacyGatewayPrefix+legacyRustFSPrefix+suffix] = replacement
		result["NOTARX_"+legacyRustFSPrefix+suffix] = replacement
	}
	return result
}()
