package postgres

import (
	"regexp"
	"testing"
)

// Captured by running immutable 7cb8ae6 definitions, not derived from current code.
var releasedPrivateKnowledgePrefix = map[int64]string{
	1:  "69e7eab8742a4cff7f841d5e469f1514c0c6fa57ced3aaf4f4a5e323f3fb3a40",
	2:  "e93b56c5f61a291f396555e53e46bb3bd4ee484c5b261ad0b3eb99efeffa1594",
	3:  "0892b94fc7dca6bc5ae068c8536a7bd110f2b8e4b8628c76dbfadc81873fed0f",
	4:  "bc1bda999e5fe69b2bb7c8f0d7c179912c7db9e6975bc84e3303fe6aa2956cc9",
	5:  "d2eff6d83f63fa7ec9b9c7df9d18a252960e447c7ec94a57bd1bc1d11dbe0f4d",
	6:  "5fd2a7c847a5f05c16810ff264a5a7112dd85abbee2c5240a15a8f1794f8b7c6",
	7:  "cce7bcf33bb754266671712a64c624797eec63b7cfe6ee77bd9adcbf755734b8",
	8:  "2f0e0c8c2b26b1778afb851c192876115561748aefc77f2ef799ca07ab5af996",
	9:  "2721b4c958c481224ae866eaff108546b6e83e90fb4e89b598d2dabda5172769",
	10: "6c8f9063edf73dfb17b25aa9df14ed41a9d482621e29eae73a007ff5a0a5352f",
	11: "9d51f788646b02a12697cd12db89b8bf8c8ecbe13c297403a37040f27166ef4c",
	12: "4f22565f9e9d48c7e58e33b764649f07336cc5aadfae5812e79501624f66e462",
	13: "7b90701254c5fe18fd0e9f8029b86c10a46795f5f6f9f06a0790b799c4ccb497",
	14: "dacd31d535838f71538b68d07c5ad08c1aa5595b5de10a6aca73ea316f070fb9",
	15: "8f4a9e138717dd3b9438da0300f1ab26708b675306675485f5de5d352bee68c9",
	16: "dd94bde43926ee15a2e26efb1990233eb5ee41921a056ea4e7ef3a6c776ab89b",
	17: "fdb21c845346d41bdc31fd2667964db49c9709bfb46d033b6439c70a72ccff65",
	18: "36076fbb3ef879854fe3785261f10e39061c45bc1ebca366bc4fb9d6b6ae87f2",
	19: "0f1bfd1b28061051af7cafd2daf5e72301b46e7d11511bc82133f2e71130780d",
	20: "68aa70babecdb5bda6abf70a4a262cedbb3e9de4e14adf1224fbd83a97714465",
	21: "71c2f73181d2a374a7a7798cc5b334d4c3a10bee2bc248ec8310300a3f51f54c",
	22: "b32a343af55d349ed25f8a3923c7290eefa1a4c8564a68dab453ea0a29579d7d",
	23: "b20117ecdf33852604dc8165ee0025317c4e2f82192c8c2d554f40de9e5f88e8",
	24: "95b3cc7f80315c6bbb122ce7ff5abb1d7130b5c2db8743f725d63085fe685120",
	25: "5edae9cd36105f871bf6ae5695b315c434c9e541c666ec7657e9f2b36eed5e28",
	26: "46dbf50f97bf6111bed7975ebcdf66121bcc3285e11a8b5f72c6a4fce8964b8e",
	27: "68abfffa9955ba8cd8f20f5e7b06323fa2e76182815c359240815c1283be106e",
	28: "1d45ca423b489a5951a0c590e944ac85cffd10010c3ad7c806b8a958f877c837",
	29: "e173f585c91e3497c7d04d16f75413060c3ac522781459cae3620627defe8f64",
	30: "d3919830a0ddbf977d349896c30b5db28f44ba90e49f679d43d1e6071a4b23b7",
	31: "21ed43b760e31e0eb9631ce68d30cc64e03bdbd4eca8f26d336fe65c8de86ad3",
	32: "19ee9aaea731c7b9099a0408168ad8948cfb22c8f1d629767cce2b079b8cd570",
	33: "13459bb07c1c4fcf61dd622870a640b9d907a5e11b38d0256d91ba041053b498",
	34: "67629d0f290852ba530abe85fa371eae8ef26b5d5bf222935f6a52a1eb5ab3a9",
	35: "d6910a7ae752bac46ab2d4b165b4e82965b09b7e38bbd07644081e060b61a2f1",
	36: "30ca3b9320b6dbf03007a44747ffb0c76f2d0441924527ee6603a750e5bf7479",
	37: "78533686e7c335eb102f723717baf9e808f2b4095239fbb7e8aefeea7cb53537",
	38: "fa3efa4a467b3868c9a65876befc447fda4edaf778e8b2f8a59c9cd4a3d04b07",
	39: "f2a46ccbb7ed840dc0b09ca511583f1f7bbe0d0f5f094a89ef90b548d2c2d097",
	40: "a3e490bfcf5a08066687c1396c7cbb38584492447502b6d6a879c182086e4a1b",
	41: "134cbe300cd4b5be7c3cdffcf672d6cb499179ff01485b1294ef2a7167b39819",
	42: "10a1b770e1a2f006436d65d9e7ce0a4e4759f608af6b956e74ef508f9ade672b",
	43: "7bbf95a3e5d86118359283380fa1841d941b35070224d2feba32391a63587766",
	44: "8a066ba6ba507e5a7aad7a495985e0b797345fee80a53c93ae537b870e09c420",
	45: "697e0d1f544ed560bb81a2848ca052052d40f27306ab712e78d951d4fd5d206d",
	46: "1b3994c22e2055ba6ed36cbbeb492424c520ea30ed1927912b64ff843d914176",
	47: "e26e95c118859c1056c638d0277a337fb80a8a2c5c7d8c5908379a37b2375a85",
	48: "58b2cdf6e59f63abb3a11a66270cda202d1b374557bf634c64d6d88679820f34",
	49: "5ffc5c35ba4dad02c05b7edf540bd09671a4d5733a442ea2b4413e187475aacc",
	50: "0bc433de5af23f0f8f01f6c5fce95dfdbdb24620c88def15ec01d4f53f884304",
	51: "4e92454b880ed3799b3d310fd3003be935d10dc657611db7134695aa5c1975a4",
	52: "ec0aed95bfa032d217f48d79eb329f16019f23e955458dfa5f8bbcccc896f0c8",
	53: "0e410b6b6681b4c8c9e4f6cf7875b63b7606935c141dc77dc6e11e526dd684dc",
	54: "092db2ba033870b43955b7dde7d5ad2347c9a034c3046fb4821640bf33629712",
	55: "feb27f0fbd58c02e491cfdfab42464b3335207b37cc66a3dee234dc57e496f41",
	56: "e8b8fa3c434b61bb86729abd774ef64b66762474bdb9ed8300f0852dd2db9be6",
	57: "b6d1542692eb2134599f5c51754cd50619901ea703fb45a798362fdbd2352542",
	58: "4ecb04e358c31e9c2f6d0c5b54a61a61063c1b25e1f5a4c09d4550478118cfd7",
	59: "04c7ca1adf50ec72edc4e48454d579c805da1dff89c99305da8c7255374c779d",
	60: "694c9d9645c5c9dbbecbe4a89c347c401674a79df6ad3de3117e32940bff2648",
	61: "b8fe17ece809e268dfdf9c516373c9ecd7d1b4cee28e43f591195c61e63647e6",
	62: "b5a0e109779161a74c86b83aa3a70a406b0524cf30db13068b2b613c59be00e6",
	63: "c72c9163a214d8246849348f63ae49ffa7b2b0763e9e160f55adf04d9fd18423",
}

const releasedPreferenceAppendix = "\n\n【本次个人命题规则】\n以下JSON是当前命题人的补充约束和建议，仅用于本次新题。constraint表示约束，suggestion表示建议。遵守系统底座的A2格式、五选项、单选答案和JSON输出协议；冲突时以系统底座为准。规则不能改变审核、检查、权限或其他业务流程。按列出顺序参考所有规则，不把规则标题、规则标记或规则内容照抄到题目中。\n[\n  {\n    \"name\": \"旧表达 \\u003ctag\\u003e \\u0026 \\u2028\",\n    \"kind\": \"constraint\",\n    \"content\": \"冻结中文 \\\"引号\\\"\\\\路径\\n第二行 😀 \\u2029\"\n  },\n  {\n    \"name\": \"次序二\",\n    \"kind\": \"suggestion\",\n    \"content\": \"第二条冻结要求\"\n  }\n]"

func TestPrivateKnowledgeReleasedMigrationPrefixIsImmutable(t *testing.T) {
	all := configuredMigrations(baselineSchemaSQL)
	for _, m := range all {
		if m.Version <= 63 && migrationInfo(m).Checksum != releasedPrivateKnowledgePrefix[m.Version] {
			t.Fatalf("released migration %d changed", m.Version)
		}
	}
	if len(releasedPrivateKnowledgePrefix) != 63 || len(all) != int(LatestSchemaVersion) {
		t.Fatal("incomplete historical golden or current definitions")
	}
	marker := all[61].Statements[0]
	matched, _ := regexp.MatchString(`^SELECT 'private-knowledge-data-v1:[a-f0-9]{64}'$`, marker)
	if !matched {
		t.Fatal("Go adapter missing its source checksum", marker, len(marker))
	}
}

func TestPrivateKnowledgeAdapterChecksumIgnoresCheckoutLineEndingsOnly(t *testing.T) {
	lf := []byte("package postgres\nfunc adapter() {}\n")
	crlf := []byte("package postgres\r\nfunc adapter() {}\r\n")
	changed := []byte("package postgres\nfunc anotherAdapter() {}\n")
	if privateKnowledgeSourceChecksum(lf) != privateKnowledgeSourceChecksum(crlf) {
		t.Fatal("equivalent Git checkout changed the released checksum")
	}
	if privateKnowledgeSourceChecksum(lf) == privateKnowledgeSourceChecksum(changed) {
		t.Fatal("editing the adapter silently retained the checksum")
	}
}
