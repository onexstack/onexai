module github.com/onexstack/onexai

go 1.27.0

require (
	github.com/a2aproject/a2a-go/v2 v2.6.0
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/brianvoe/gofakeit/v7 v7.15.0
	github.com/casdoor/casdoor-go-sdk v1.47.0
	github.com/cloudwego/eino v0.9.21
	github.com/cloudwego/eino-ext/adk/backend/local v0.2.6
	github.com/cloudwego/eino-ext/components/model/ark v0.1.71
	github.com/cloudwego/eino-ext/components/model/claude v0.1.26
	github.com/cloudwego/eino-ext/components/model/deepseek v0.1.7
	github.com/cloudwego/eino-ext/components/model/gemini v0.1.36
	github.com/cloudwego/eino-ext/components/model/ollama v0.1.9
	github.com/cloudwego/eino-ext/components/model/openai v0.1.13
	github.com/cloudwego/eino-ext/components/model/qianfan v0.1.4
	github.com/cloudwego/eino-ext/components/model/qwen v0.1.9
	github.com/cloudwego/eino-ext/components/tool/mcp v0.0.9
	github.com/dgraph-io/ristretto/v2 v2.3.0
	github.com/eino-contrib/jsonschema v1.0.3
	github.com/feizaonet/cobrax v0.0.1
	github.com/gammazero/workerpool v1.2.2
	github.com/gin-contrib/pprof v1.5.6
	github.com/gin-gonic/gin v1.12.0
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/google/wire v0.7.0
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674
	github.com/larksuite/oapi-sdk-go/v3 v3.9.10
	github.com/looplab/fsm v1.0.4
	github.com/mark3labs/mcp-go v0.43.2
	github.com/mattn/go-runewidth v0.0.31
	github.com/minio/minio-go/v7 v7.2.1
	github.com/onexstack/onexstack v1.0.8
	github.com/onexstack/protoc-gen-defaults v0.0.2
	github.com/open-policy-agent/opa v1.9.0
	github.com/pkoukk/tiktoken-go v0.1.8
	github.com/pkoukk/tiktoken-go-loader v0.0.2
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/common v0.70.1
	github.com/redis/go-redis/v9 v9.7.3
	github.com/robfig/cron/v3 v3.0.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/spf13/viper v1.21.0
	github.com/stretchr/testify v1.12.1
	github.com/tencentyun/cos-go-sdk-v5 v0.7.75
	github.com/tmc/langchaingo v0.1.15
	github.com/twmb/franz-go v1.20.7
	gitlab.com/gitlab-org/api/client-go v1.46.0
	go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin v0.70.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.45.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.uber.org/automaxprocs v1.6.0
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.22.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/text v0.43.0
	golang.org/x/time v0.16.0
	golang.org/x/tools v0.52.0
	google.golang.org/genai v1.70.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/mysql v1.6.0
	gorm.io/driver/postgres v1.5.11
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.2
	k8s.io/api v0.38.0-alpha.2
	k8s.io/apiextensions-apiserver v0.38.0-alpha.2
	k8s.io/apimachinery v0.38.0-alpha.2
	k8s.io/apiserver v0.38.0-alpha.2
	k8s.io/client-go v0.38.0-alpha.2
	k8s.io/component-base v0.38.0-alpha.2
	k8s.io/controller-manager v0.38.0-alpha.2
	k8s.io/klog/v2 v2.140.0
	k8s.io/kube-aggregator v0.0.0
	k8s.io/kube-openapi v0.0.0-20261014044854-ca3452a79603
	k8s.io/kubectl v0.38.0-alpha.2
	k8s.io/kubernetes v1.38.0-alpha.2
	k8s.io/utils v0.0.0-20260707023825-cf1189d6abe3
	resty.dev/v3 v3.0.0-rc.4
	sigs.k8s.io/controller-runtime v0.25.2
	sigs.k8s.io/structured-merge-diff/v7 v7.0.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	cel.dev/expr v0.25.2 // indirect
	cloud.google.com/go v0.116.0 // indirect
	cloud.google.com/go/auth v0.20.0 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.1 // indirect
	cyphar.com/go-pathrs v0.2.5 // indirect
	filippo.io/edwards25519 v1.1.1 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20261013165250-51075c6ea6bc // indirect
	github.com/MakeNowJust/heredoc v1.0.0 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/alibabacloud-go/alibabacloud-gateway-pop v0.0.9 // indirect
	github.com/alibabacloud-go/alibabacloud-gateway-spi v0.0.5 // indirect
	github.com/alibabacloud-go/darabonba-array v0.1.0 // indirect
	github.com/alibabacloud-go/darabonba-encode-util v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-map v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-openapi/v2 v2.0.12 // indirect
	github.com/alibabacloud-go/darabonba-signature-util v0.0.7 // indirect
	github.com/alibabacloud-go/darabonba-string v1.0.2 // indirect
	github.com/alibabacloud-go/debug v1.0.1 // indirect
	github.com/alibabacloud-go/endpoint-util v1.1.1 // indirect
	github.com/alibabacloud-go/kms-20160120/v3 v3.2.3 // indirect
	github.com/alibabacloud-go/openapi-util v0.1.2 // indirect
	github.com/alibabacloud-go/tea v1.2.2 // indirect
	github.com/alibabacloud-go/tea-utils/v2 v2.0.9 // indirect
	github.com/alibabacloud-go/tea-xml v1.1.3 // indirect
	github.com/aliyun/alibaba-cloud-sdk-go v1.61.1843 // indirect
	github.com/aliyun/alibabacloud-dkms-gcs-go-sdk v0.5.1 // indirect
	github.com/aliyun/alibabacloud-dkms-transfer-go-sdk v0.1.9 // indirect
	github.com/aliyun/aliyun-secretsmanager-client-go v1.1.5 // indirect
	github.com/aliyun/credentials-go v1.4.13 // indirect
	github.com/anthropics/anthropic-sdk-go v1.56.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/armon/go-metrics v0.4.1 // indirect
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/aws/aws-sdk-go-v2 v1.36.6 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.11 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.29.18 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.71 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.33 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.18 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.25.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.30.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.34.1 // indirect
	github.com/aws/smithy-go v1.22.5 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/baidubce/bce-qianfan-sdk/go/qianfan v0.0.15 // indirect
	github.com/baidubce/bce-sdk-go v0.9.274 // indirect
	github.com/benbjohnson/clock v1.3.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.3 // indirect
	github.com/bradfitz/gomemcache v0.0.0-20260422231931-4d751bb6e37c // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.4 // indirect
	github.com/bytedance/sonic/loader v0.5.2 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/chai2010/gettext-go v1.0.3 // indirect
	github.com/clbanning/mxj v1.8.4 // indirect
	github.com/clbanning/mxj/v2 v2.5.7 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/cloudwego/eino-ext/libs/acl/openai v0.1.17 // indirect
	github.com/cohesion-org/deepseek-go v1.3.4 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/deckarep/golang-set v1.7.1 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/dlclark/regexp2 v1.12.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/eino-contrib/ollama v0.1.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/exponent-io/jsonpath v0.0.0-20210407135951-1de76d718b3f // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.6 // indirect
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/gammazero/deque v1.2.2 // indirect
	github.com/gin-contrib/sse v1.1.2 // indirect
	github.com/go-errors/errors v1.5.1 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.5 // indirect
	github.com/go-kratos/kratos/contrib/registry/consul/v2 v2.0.0-20260404020628-f149714c1d54 // indirect
	github.com/go-kratos/kratos/contrib/registry/etcd/v2 v2.0.0-20260404020628-f149714c1d54 // indirect
	github.com/go-kratos/kratos/contrib/registry/eureka/v2 v2.0.0-20260404020628-f149714c1d54 // indirect
	github.com/go-kratos/kratos/contrib/registry/nacos/v2 v2.0.0-20260404020628-f149714c1d54 // indirect
	github.com/go-kratos/kratos/v2 v2.9.2 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v1.0.2 // indirect
	github.com/go-openapi/jsonreference v1.0.3 // indirect
	github.com/go-openapi/swag/conv v0.29.2 // indirect
	github.com/go-openapi/swag/jsonutils v0.29.2 // indirect
	github.com/go-openapi/swag/pools v0.29.2 // indirect
	github.com/go-openapi/swag/typeutils v0.29.2 // indirect
	github.com/go-playground/form/v4 v4.2.1 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.5 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-zookeeper/zk v1.0.4 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/cel-go v0.29.2 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.23 // indirect
	github.com/googleapis/gax-go/v2 v2.22.0 // indirect
	github.com/goph/emperror v0.17.2 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/gosuri/uitable v0.0.4 // indirect
	github.com/grafana/regexp v0.0.0-20250905093917-f7b3be9d1853 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus v1.1.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/hashicorp/consul/api v1.31.2 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-metrics v0.6.1 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/hashicorp/serf v0.10.4 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/jinzhu/copier v0.4.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/jolestar/go-commons-pool/v2 v2.1.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/klippa-app/go-pdfium v1.19.8 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.0.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc/v3 v3.0.6 // indirect
	github.com/lestrrat-go/jwx/v3 v3.0.13 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/liggitt/tabwriter v0.0.0-20181228230101-89fcab3d43de // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	github.com/meguminnnnnnnnn/go-openai v0.1.6 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/moby/term v0.5.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/monochromegane/go-gitignore v0.0.0-20200626010858-205db1a8cc00 // indirect
	github.com/montanaflynn/stats v0.7.1 // indirect
	github.com/mozillazg/go-httpheader v0.2.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f // indirect
	github.com/nacos-group/nacos-sdk-go/v2 v2.3.5 // indirect
	github.com/natefinch/lumberjack v2.0.0+incompatible // indirect
	github.com/nikolalohinski/gonja v1.5.3 // indirect
	github.com/nsqio/go-nsq v1.1.0 // indirect
	github.com/ollama/ollama v0.6.8 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/selinux v1.15.1 // indirect
	github.com/orcaman/concurrent-map v0.0.0-20210501183033-44dafcb38ecc // indirect
	github.com/pb33f/go-yaml v0.1.1 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.2 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/peterbourgon/diskv/v3 v3.0.1 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/polarismesh/grpc-go-polaris v1.5.0 // indirect
	github.com/polarismesh/polaris-go v1.6.1 // indirect
	github.com/polarismesh/specification v1.5.5-alpha.1 // indirect
	github.com/prometheus/otlptranslator v0.0.2 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.61.0 // indirect
	github.com/rabbitmq/amqp091-go v1.14.0 // indirect
	github.com/rcrowley/go-metrics v0.0.0-20250401214520-65e299d6c5c9 // indirect
	github.com/redis/go-redis/extra/rediscensus/v9 v9.7.3 // indirect
	github.com/redis/go-redis/extra/rediscmd/v9 v9.7.3 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/slongfield/pyfmt v0.0.0-20220222012616-ea85ff4c361f // indirect
	github.com/sony/sonyflake v1.2.1 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/tchap/go-patricia/v2 v2.3.3 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.2 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tinylib/msgp v1.6.5 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.12.0 // indirect
	github.com/ugorji/go/codec v1.3.2 // indirect
	github.com/valyala/fastjson v1.6.10 // indirect
	github.com/vektah/gqlparser/v2 v2.5.63 // indirect
	github.com/volcengine/volc-sdk-golang v1.0.257 // indirect
	github.com/volcengine/volcengine-go-sdk v1.2.56 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.2.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xlab/treeprint v1.2.0 // indirect
	github.com/yargevad/filepathx v1.0.0 // indirect
	github.com/yashtewari/glob-intersection v0.2.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.etcd.io/etcd/api/v3 v3.7.2 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.7.2 // indirect
	go.etcd.io/etcd/client/v3 v3.7.2 // indirect
	go.mongodb.org/mongo-driver v1.17.10 // indirect
	go.mongodb.org/mongo-driver/v2 v2.8.2 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.71.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.38.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.60.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.38.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.46.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/ratelimit v0.3.1 // indirect
	go.uber.org/zap v1.27.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.29.0 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.4.0 // indirect
	google.golang.org/api v0.278.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/grpc v1.84.0 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/ini.v1 v1.67.3 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/cli-runtime v0.38.0-alpha.2 // indirect
	k8s.io/cloud-provider v0.38.0-alpha.2 // indirect
	k8s.io/cluster-bootstrap v0.0.0 // indirect
	k8s.io/component-helpers v0.38.0-alpha.2 // indirect
	k8s.io/csi-translation-lib v0.0.0 // indirect
	k8s.io/dynamic-resource-allocation v0.38.0-alpha.2 // indirect
	k8s.io/endpointslice v0.0.0 // indirect
	k8s.io/kms v0.38.0-alpha.2 // indirect
	k8s.io/kube-controller-manager v0.0.0 // indirect
	k8s.io/kube-proxy v0.0.0 // indirect
	k8s.io/kube-scheduler v0.0.0 // indirect
	k8s.io/kubelet v0.38.0-alpha.2 // indirect
	k8s.io/metrics v0.38.0-alpha.2 // indirect
	k8s.io/mount-utils v0.0.0 // indirect
	k8s.io/pod-security-admission v0.0.0 // indirect
	k8s.io/streaming v0.38.0-alpha.2 // indirect
	sigs.k8s.io/apiserver-network-proxy/konnectivity-client v0.37.0 // indirect
	sigs.k8s.io/json v0.0.0-20260909141634-11ed52e25bc5 // indirect
	sigs.k8s.io/kustomize/api v0.21.3 // indirect
	sigs.k8s.io/kustomize/kyaml v0.21.3 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
)

// grpc-go-polaris is vendored to add the balancer.Balancer.ExitIdle method that
// every released version lacks; see third_party/grpc-go-polaris/LOCAL-PATCH.md.
replace github.com/polarismesh/grpc-go-polaris => ./third_party/grpc-go-polaris

// cobrax is vendored for redirect_unix.go: syscall.Dup2 has no linux/arm64
// counterpart, so v0.0.1 could not build that target. The fork uses
// golang.org/x/sys/unix, which shims Dup2 onto dup3; see
// third_party/cobrax/LOCAL-PATCH.md.
replace github.com/feizaonet/cobrax => ./third_party/cobrax

// k8s.io/kubernetes references its staging repositories at v0.0.0, so every
// module it pulls in must be replaced with the matching release. The version
// here must equal the k8s.io/* pins above (v0.38.0-alpha.2); k8s.io/kubernetes
// itself is tagged v1.x.
replace (
	k8s.io/api => k8s.io/api v0.38.0-alpha.2
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.38.0-alpha.2
	k8s.io/apimachinery => k8s.io/apimachinery v0.38.0-alpha.2
	k8s.io/apiserver => k8s.io/apiserver v0.38.0-alpha.2
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.38.0-alpha.2
	k8s.io/client-go => k8s.io/client-go v0.38.0-alpha.2
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.38.0-alpha.2
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.38.0-alpha.2
	k8s.io/code-generator => k8s.io/code-generator v0.38.0-alpha.2
	k8s.io/component-base => k8s.io/component-base v0.38.0-alpha.2
	k8s.io/component-helpers => k8s.io/component-helpers v0.38.0-alpha.2
	k8s.io/controller-manager => k8s.io/controller-manager v0.38.0-alpha.2
	k8s.io/cri-api => k8s.io/cri-api v0.38.0-alpha.2
	k8s.io/cri-client => k8s.io/cri-client v0.38.0-alpha.2
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.38.0-alpha.2
	k8s.io/dynamic-resource-allocation => k8s.io/dynamic-resource-allocation v0.38.0-alpha.2
	k8s.io/endpointslice => k8s.io/endpointslice v0.38.0-alpha.2
	k8s.io/kms => k8s.io/kms v0.38.0-alpha.2
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.38.0-alpha.2
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.38.0-alpha.2
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.38.0-alpha.2
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.38.0-alpha.2
	k8s.io/kubectl => k8s.io/kubectl v0.38.0-alpha.2
	k8s.io/kubelet => k8s.io/kubelet v0.38.0-alpha.2
	k8s.io/kubernetes => k8s.io/kubernetes v1.38.0-alpha.2
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.38.0-alpha.2
	k8s.io/metrics => k8s.io/metrics v0.38.0-alpha.2
	k8s.io/mount-utils => k8s.io/mount-utils v0.38.0-alpha.2
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.38.0-alpha.2
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.38.0-alpha.2
)

// TODO(temporary): verify the local onexstack change where genericstore.Get no
// longer logs gorm.ErrRecordNotFound as an error. Drop this replace and bump to
// the released version once it is tagged.
replace github.com/onexstack/onexstack => ../onexstack
