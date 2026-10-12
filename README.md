# Juicebox Software Realm

This project contains all the software necessary to run a Juicebox Software Realm on the hosting provider of your choice.

We recommend adding at least one software backed realm to your configuration alongside Juicebox's hardware backed realms. Utilizing realms hosted in different cloud providers and maintained by different authorities – in addition to Juicebox's hardware backed realms – can increase the security of your user secrets. With this diversity in your configuration, you would have to compromise at least `recover_threshold` different realm providers before you could even attempt to recover a secret (and you'd _still_ have to gain access to their PIN in order to decrypt the secret).

## Usage

```
Usage of jb-sw-realm:
  -id string
    	A 16-byte hex string identifying this realm. (default random)

    	Note: Changing this id for an existing realm will result in data loss.
  -port int
    	The port to run the server on. (default 8080)
  -provider string
        The provider to use. [gcp|aws|mongo|memory] (default "memory")

    	Some providers take additional configuration via environment variables.

    	gcp:
    	    BIGTABLE_INSTANCE_ID = The id of your Bigtable instance in GCP
    	    GCP_PROJECT_ID       = The id of your project in GCP
    	aws:
    	    AWS_REGION_NAME      = The name of the region your AWS instance is in

    	    Note: AWS uses DynamoDB and assumes you have a table created with a name
    	          matching your realm id and a partitionKey named recordID.
	mongo:
	    MONGO_URL = The url to acess your MongoDB instance in the form of:
	                mongodb://username:password@host:port/database
	    VOLCENGINE_REGION = The Volcengine region for KMS (for example, "cn-beijing")
	    VOLCENGINE_KMS_KEY_ID = The Volcengine KMS key ID used to encrypt Mongo data
	    VOLCSTACK_ACCESS_KEY_ID / VOLCSTACK_SECRET_ACCESS_KEY (and optional VOLCSTACK_SESSION_TOKEN)
	      are used by the Volcengine SDK for KMS auth.


	    Note: User records are stored in a collection named "userRecords".
    	    Tenant signing keys are stored in a collection named "tenantSecrets".
    	memory:
    	    TENANT_SECRETS = The versioned tenant secrets, in JSON format.
    	                     For example: {"tenantName":{"1":"tenantSecretKey"}}
```

Compile the Juicebox Software Realm with `go build ./cmd/jb-sw-realm` or compile and run with `go run ./cmd/jb-sw-realm/`.

To quickly spin up a local realm for testing, you can run:

 ```sh
TENANT_SECRETS='{"test":{"1":"an-auth-token-key"}}' jb-sw-realm -port 8080
 ```

## Running a Realm

This software is setup to be portable to the cloud provider of your choice.

In order to run a realm you will generally need to:
1. Clone this repo
2. Choose a provider to manage your data
3. Initialize the provider environment with Terraform
4. Upload the realm software you to your provider

## Tenant Auth Secrets

Realm software is structured to operate in a multi-tenant environment. In order to validate access from different tenants, your realm will need access to the JWT signing key for each tenant you plan to support. You may add support for additional tenants at any time in the future.

In general, regardless of provider, tenant secrets are stored in the following fashion:

```json
{
	"jb-sw-tenant-{{yourTenantName}}" : {"{{keyVersion}}" : "{{yourSigningKey}}"},
	"jb-sw-tenant-acme" : {"1" : "acme-tenant-secret"},
}
```

Note: Tenant names should be unique alphanumeric strings. Key versions should be non-negative integer values, which are generally incremented when you upload a new key for a tenant.

The realm software will determine which tenant key to validate on a request by accessing the "kid" header field on a received JWT auth token. This field will be provided in the format of `tenantName:1`.

### Ed25519 租户密钥（OneKey 部署）

**realmId**：每个 realm 的 ID 是随机生成的 16 字节，写成 32 位 hex，例如：

```sh
openssl rand -hex 16
```

部署新 realm 时生成一次，然后配到这几个地方：realm 的 `REALM_ID`、service-juicebox 的 `REALM_IDS` 和 `REALM_TENANT_PRIVATE_KEY`（私钥 map 的 key）。

realmId 上线之后**不能再改**：

* realm 要求 JWT 的 `aud` 等于自己的 realmId，service-juicebox 也按它查找签名私钥，改了之后所有请求都会鉴权失败；
* 每条用户记录在 KMS 加密时都绑定了 realmId，改了之后已有记录全部无法解密，等于数据丢失。

另外，realm 启动时如果既没有 `-id` 参数也没有 `REALM_ID` 环境变量，会自己随机生成一个新 ID，所以正式环境的 env 文件里必须固定写好 `REALM_ID`。

我们的 realm 用 Ed25519（EdDSA）签名的 JWT 鉴权。每个 realm 有一对独立的密钥：

* **私钥**在 service-juicebox，用来签发访问这个 realm 的 JWT；
* **公钥**在这个 realm 的 Mongo `tenantSecrets` 集合，用来验证 JWT。

| 位置 | 内容 | 格式 |
|---|---|---|
| service-juicebox 的 `REALM_TENANT_PRIVATE_KEY` | 以 realmId 为 key 的 map，每个值是该 realm 的私钥 | PKCS#8 DER 的 hex，96 个字符，以 `302e020100300506032b657004220420` 开头 |
| realm 的 `tenantSecrets` 文档里 `secret` 的 `data` | 对应的公钥 | SPKI DER 的 hex，88 个字符，以 `302a300506032b6570032100` 开头 |
| `tenantSecrets` 文档的 `_id` | `jb-sw-tenant-` 加 service-juicebox 的 `REALM_TENANT_NAME`，例如 `jb-sw-tenant-acme` | |
| `tenantSecrets` 文档的 `version` | service-juicebox 的 `REALM_TENANT_VERSION`。JWT header 的 `kid` 形如 `acme:1`，冒号后面就是这个版本 | |

**生成密钥**：每个 realm 各生成一对。用 Node：

```js
const { generateKeyPairSync } = require("node:crypto");
const { privateKey, publicKey } = generateKeyPairSync("ed25519");
console.log("私钥:", privateKey.export({ format: "der", type: "pkcs8" }).toString("hex"));
console.log("公钥:", publicKey.export({ format: "der", type: "spki" }).toString("hex"));
```

或者用 OpenSSL 3：

```sh
openssl genpkey -algorithm ed25519 -outform DER -out realm.key.der
xxd -p realm.key.der | tr -d '\n'                                                        # 私钥
openssl pkey -inform DER -in realm.key.der -pubout -outform DER | xxd -p | tr -d '\n'   # 公钥
```

**写入 realm**：在该 realm 的库里执行，`<公钥>` 换成上面生成的公钥 hex：

```js
db.tenantSecrets.insertOne({
  _id: "jb-sw-tenant-<REALM_TENANT_NAME>",
  version: <REALM_TENANT_VERSION>,
  secret: "{\"data\":\"<公钥>\",\"encoding\":\"Hex\",\"algorithm\":\"Edwards25519\"}"
})
```

私钥按 realmId 写进 service-juicebox 的 `REALM_TENANT_PRIVATE_KEY`。私钥只放在 service-juicebox 的配置里，不要写进文档或聊天记录。

**核对一对密钥是否匹配**：由私钥推出公钥，和 `tenantSecrets` 里的 `data` 对比。这一步要用正式环境的私钥，由 SRE 执行：

```js
const { createPrivateKey, createPublicKey } = require("node:crypto");
const priv = createPrivateKey({ key: Buffer.from("<私钥>", "hex"), format: "der", type: "pkcs8" });
console.log(createPublicKey(priv).export({ format: "der", type: "spki" }).toString("hex"));
```

**更换密钥**：`REALM_TENANT_VERSION` 是所有 realm 共用的，所以要一起换。

1. 为每个 realm 生成新的一对密钥。
2. 在**每个 realm** 的 `tenantSecrets` 里插入新版本（例如 `version: 2`）的公钥，旧版本先保留。
3. 把 service-juicebox 的 `REALM_TENANT_PRIVATE_KEY` 换成新私钥，`REALM_TENANT_VERSION` 改成新版本，然后重启。

realm 按 JWT `kid` 里的版本号找公钥，所以切换期间新旧版本的 JWT 都能通过验证。确认没有旧版本的请求之后，再删除旧版本的文档。

## GCP

The following instructions will help you quickly deploy a realm to Google's App Engine Flex.

Before you begin, setup a project for your realm to run in on [console.cloud.google.com](https://console.cloud.google.com) and make note of its ID.

Next, setup your project environment with terraform as follows:
```sh
cd gcp
terraform init
terraform plan -var='tenant_secrets={"acme":"acme-tenant-key","anotherTenant":"another-tenant-key"}'
terraform apply -var='tenant_secrets={"acme":"acme-tenant-key","anotherTenant":"another-tenant-key"}'
```

Note: you should update the tenant secrets `var` to reflect the actual secrets you wish to support.

After terraform has finished configuring your project environment, you should see an output like follows:
```sh
BIGTABLE_INSTANCE_ID = "jb-sw-realms"
GCP_PROJECT_ID = "your-project-id"
REALM_ID = "99b2da84-b707-6203-dc35-804bbbcb8cba"
SERVICE_ACCOUNT = "jb-sw-realms@your-project-id.iam.gserviceaccount.com"
```

Open the `cmd/jb-sw-realm/app.yaml` file and configure it with these values where appropriate, for example:
Replace `{{YOUR_BIGTABLE_INSTANCE_ID}}` with `jb-sw-realms`.

Finally, you can deploy the realm software by running the following command from the `cmd/jb-sw-realm` directory of the repo:
```sh
gcloud app deploy --project {{YOUR_GCP_PROJECT_ID}}
```

Note: you will need to have the `gcloud` command line tools installed to execute this command. You can find instructions on installing these [here](https://cloud.google.com/sdk/docs/install).

This may take a few minutes, but upon success you should be able to access your realm at:
https://{{YOUR_GCP_PROJECT_ID}}.wl.r.appspot.com

If all was successful, you'll see a page render that looks something like:
```json
{"realmID":"99b2da84-b707-6203-dc35-804bbbcb8cba"}
```

If you wish to configure a custom domain for your new realm, visit:
https://console.cloud.google.com/appengine/settings/domains

## AWS

The following instructions will help you quickly deploy a realm to Amazon's Elastic Beanstalk.

To begin, initialize your environment with terraform as follows:
```sh
cd aws
terraform init
terraform plan -var='tenant_secrets={"acme":"acme-tenant-key","anotherTenant":"another-tenant-key"}'
terraform apply -var='tenant_secrets={"acme":"acme-tenant-key","anotherTenant":"another-tenant-key"}'
```

Note: you should update the tenant secrets `var` to reflect the actual secrets you wish to support.

After terraform has finished configuring your project environment, you should see an output like follows:
```sh
CLOUDFRONT_DOMAIN = "https://d1oivazt933sey.cloudfront.net"
```

You can view your newly deploy Elastic Beanstalk environment at the returned URL, but for now it's just going to render an example application.

To deploy the realm software and replace the example site, you'll need to install the `eb` command line tools. You can find instructions on how to install these [here](https://docs.aws.amazon.com/elasticbeanstalk/latest/dg/eb-cli3-install.html).

Once installed, from the root of the repo run the command:
```sh
eb init
```

You will be presented with a number of prompts. In general, you can select the default options *except* for the following questions:
1. Use the region you selected in Terraform
2. Choose the existing "jb-sw-realm" as the application to use
3. Choose the existing "jb-sw-realm" as the environment to use

Once initialized, you can now deploy the realm software by running the following command from the root of the repo:
```sh
eb deploy jb-sw-realm
```

This may take a few minutes, but upon success you should be able to access your realm using the URL returned by terraform earlier.

If all was successful, you'll see a page render that looks something like:
```json
{"realmID":"99b2da84-b707-6203-dc35-804bbbcb8cba"}
```

To finish setup and start using your realm, you will want to configure your CloudFront distribution with a custom domain. You can do so by visiting:
https://console.aws.amazon.com/cloudfront

## Other Providers

If you'd like to host a realm on another provider, or with a configuration other than the ones described above, you will need to choose a provider and manually configure your environment.

For your convenience, a Dockerfile is provided that can run a realm on most hosting platforms once properly configured.

The available configuration variables, beyond the args on the `jb-sw-realm` binary are as follows:

* **REALM_ID**: A unique ID representing your realm. This is ignored if the `-id` flag is specified.
* **PROVIDER**: The provider you wish to use [gcp|aws|mongo|memory]. This is ignored if the `-provider` flag is specified.
* **BIGTABLE_INSTANCE_ID**: The id of your bigtable instance in GCP. This is only read when using the `GCP` provider.
* **GCP_PROJECT_ID**: The id of your project in GCP. This is only read when using the `GCP` provider.
* **AWS_REGION_NAME**: The name of your region in AWS. This is only read when using the `AWS` provider.
* **MONGO_URL**: The fully qualified URL to your mongo database, such as `mongodb://username:password@host:port/database`. This is only read when using the `Mongo` provider.
* **VOLCENGINE_REGION**: The Volcengine region for KMS, such as `cn-beijing`. This is only read when using the `Mongo` provider.
* **VOLCENGINE_KMS_KEY_ID**: The Volcengine KMS key ID used to encrypt Mongo data. This is only read when using the `Mongo` provider.
* **VOLCSTACK_ACCESS_KEY_ID** / **VOLCSTACK_SECRET_ACCESS_KEY**: Volcengine credentials used by the KMS SDK when using the `Mongo` provider. `VOLCSTACK_SESSION_TOKEN` is optional.
* **RECORD_HISTORY_ENABLED**: 设为 `true` 后，每次用户记录变更都会备份到 `userRecordHistory` 集合，见 [Record History](#record-history)。仅 `Mongo` provider 生效。
* **TENANT_SECRETS**: A list of versioned tenant secrets in the form of `'{"test":{"1":"an-auth-token-key"}}'`. This is only used if the `memory` provider is specified.
* **OPENTELEMETRY_ENDPOINT**: The URL to an OpenTelemetry gRPC service where tracing data should be sent.

## Record History

设置 `RECORD_HISTORY_ENABLED=true` 后（仅 Mongo provider 生效），每次 `userRecords` 写入成功，都会在同一个数据库的 `userRecordHistory` 集合里存一份备份。

用途是在两个 realm 数据不一致时，把其中一个 realm 回滚。典型场景：重置 PIN 时 `Register2` 只写入了一个 realm，两个 realm 的 `Registered.Version` 不一致，新旧 PIN 都无法恢复钱包。

**对线上请求没有影响：**

* 备份在记录写入成功之后才做，并且在后台执行。
* 备份失败只会打日志，并计入指标 `realm.record_history.count`。指标的 `outcome` 标签取值为 `ok`（成功）、`error`（失败）、`dropped`（积压过多被丢弃）。
* 备份集合初始化失败时，realm 照常启动，只是没有备份。

每条备份的结构：

```js
{
  userRecordId: "<userRecords 的 _id>",
  realmId: "<realm id>",
  archivedAt: ISODate(...),        // 备份时间
  previousVersion: NumberLong(5),  // 写入前 userRecords 的 version，首次创建时为 null
  newVersion: NumberLong(6),       // 写入后的 version
  versionChanged: true,            // 写入前后 registeredVersionHash 不同，例如 Register2（注册 / 重置 PIN）
  record: { /* 写入前的 userRecords 完整文档，原样拷贝，仍是加密的 */ },
  before: { registrationState: "Registered", registeredVersionHash: "<64 位 hex>", guessCount: 0, numGuesses: 10 },
  after:  { registrationState: "Registered", registeredVersionHash: "<64 位 hex>", guessCount: 0, numGuesses: 10 }
}
```

* `record` 可以原样写回 `userRecords`，恢复到这次写入之前的状态。
* `registeredVersionHash` 是内层 `Registered.Version`（16 字节）的 sha256，存成 64 位 hex 字符串，方便直接复制到查询条件里。恢复钱包时，两个 realm 必须一致的就是这个值；状态不是 `Registered` 时为 null。文档外层的 `version` 只是单个 realm 的写入计数，两边不需要一致。
* `before` / `after` 不含密钥，但 `guessCount`、`numGuesses` 属于安全状态，这个集合只能内部访问。
* 每次写入都会记录，包括每次输入 PIN。只看注册变化时，加条件 `versionChanged: true`。

**已知限制：**

* 同一用户的两次写入并发冲突时，Mongo 会静默丢弃其中一次写入，但备份仍会记录。这种记录会和另一条记录的 `previousVersion` 相同。
* 进程退出时还没写完的备份会丢失。
* 直接在 `userRecords` 上手工修改不会自动产生备份。下面的恢复流程会在修改前手动备份当前记录（`manualBackup: true`）。
* 备份里的 `record` 用 realm 的 KMS 密钥加密。只要还有备份可能用到某个 KMS 密钥，就不能删除或停用它。
* realm 和 service-juicebox 都不能按单个用户禁止写入，下面的恢复流程只能靠通知用户、等待和两侧核对来避免并发写入。第 7 步核对通过之后、用户登录之前，仍有很小的窗口可能被用户之前在途的请求打破，所以第 8 步用户恢复后还要再核对一次。

### 部署

1. **开启备份**：在 realm 的 systemd 服务 `EnvironmentFile` 指定的 env 文件里加上 `RECORD_HISTORY_ENABLED=true`，然后重启服务。两个 realm 都要加。备份相关的日志以 `record history:` 开头，写在 realm 的日志文件里。
2. **部署恢复工具**：按编译 realm 的方式编译两个命令行工具，和 realm 二进制放在同一个目录：
   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/record_id ./cmd/record_id
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/record_state ./cmd/record_state
   ```
   它们只在恢复时手动执行，不需要配置成服务。下面恢复流程里的命令都在这个目录下执行。

### 恢复用户记录

整个流程由 SRE 执行，在两个地方操作：

* **realm 服务器上**：登录 realm 服务器，执行上面部署的 `record_id` 和 `record_state`。`record_state` 通过 `-env-file` 直接读取 realm 自己的 env 文件（systemd 服务 `EnvironmentFile` 指定的文件，可以用 `systemctl cat <realm 服务名>` 查看路径），拿到 `MONGO_URL`、KMS 凭证、`REALM_ID`，不需要手动填写这些敏感值。不要用 `source` 加载这个 env 文件：`MONGO_URL` 里的 `&` 会被 shell 解析错。
* **DBW 里**：在火山引擎 DBW（数据库工作台）的 MongoDB 数据交互台执行数据库命令。

**流程里要替换的值**：命令里的 `<...>` 都要换成实际值，每个值从哪来见下表。

| 值 | 含义 | 从哪来 |
|---|---|---|
| `<recordId>` | 这个用户在 realm 库里的记录 ID，64 位十六进制，两个 realm 相同 | 第 1 步 |
| 要回滚的 realm | 两个 realm 中需要回滚的那一个，以 `record_state` 输出的 `realmId` 和 `database`（库名）区分 | 第 3 步 |
| `<hash>` | 回滚目标：**另一个 realm** 当前的 `registeredVersionHash` | 第 3 步 |
| `<V>` | **要回滚的 realm** 当前记录的 `version` | 第 3 步 |
| `<historyId>`、`<edk>` | 要恢复的那条备份的 `_id`，以及它的 `record.encryptedDataKey` | 第 4 步 |
| `<backupId>`、`<backupEdk>` | 恢复前给当前记录做的备份的 `_id`，以及它的 `record.encryptedDataKey`，用于撤销 | 第 5 步 |

**在 DBW 里执行命令的注意事项**

* 一次只执行一条命令。DBW 一次执行多条时只显示最后一条的结果。
* 两个 realm 是两个不同的数据库，执行前确认当前连的是第 3 步确定的那个库。
* 恢复命令用到了 `$merge`，需要 MongoDB 4.2 及以上。
* 任何一步的结果和预期不符，都停下来找开发，不要继续往下执行。

#### 第 1 步 算出 `<recordId>`（任意一台 realm 服务器上）

需要准备：

* `<tenant>`：service-juicebox 的配置项 `REALM_TENANT_NAME`。所有用户都一样，注意用对应环境（正式 / 测试）的值。
* `<provider>`：出问题的钱包是用哪种账号登录的，`google` 或 `apple`。先通过客服向用户确认，例如"无法登录谷歌钱包"就是 `google`。同一个用户的 Google 和 Apple 账号对应两个不同的钱包，不能混用。
* `<provider_id>`：该用户这个 provider 账号对应的 `provider_id`。
* service-juicebox 的配置项 `USER_ID_SALT`（敏感值）。

在 bash 里执行（`sh` 不支持 `read -s`）。第一条命令会等待输入 salt，输入内容不会显示，也不会留在命令历史里：

```sh
read -rs USER_ID_SALT && export USER_ID_SALT
./record_id -tenant <tenant> -provider <provider> -provider-id <provider_id>
```

如果用户用的是测试网钱包，再加上 `-testnet`。输出的 64 位十六进制字符串就是 `<recordId>`。

确认算对了：在 DBW 里两个 realm 的库都执行下面这条，都应该能查到 1 条记录。查不到说明某个值不对，例如 provider 选错了。如果无法向用户确认 provider，而 Google 和 Apple 算出的两个 recordId 都能查到记录，停下来找开发，不要任选一个继续。

```js
db.userRecords.find({ _id: "<recordId>" })
```

#### 第 2 步 暂停该用户的写入

1. 通过客服通知用户：恢复完成之前，不要打开钱包登录，也不要重置 PIN。
2. 等待至少 10 分钟，让用户之前发出、还在途中的请求处理完。
3. 在 DBW 里两个 realm 的库都执行下面这条，确认最新一条的 `archivedAt` 早于 10 分钟前，也就是最近没有新的写入。如果还有新的写入，继续等待，并再次确认用户没有在操作：
   ```js
   db.userRecordHistory.find({ userRecordId: "<recordId>" }).sort({ archivedAt: -1 }).limit(1)
   ```

后面的恢复命令带版本检查：即使仍有写入，也只会不执行，而不会覆盖用户的新数据。

#### 第 3 步 查两个 realm 的当前版本，确定回滚方向（两台 realm 服务器上）

在**两台 realm 服务器上各执行一次**。env 文件里有敏感值，通常只有 root 或 realm 的运行用户能读，所以一般需要 `sudo`：

```sh
sudo ./record_state -env-file <env 文件> -record <recordId>
```

输出示例：

```json
{
  "realmId": "6b47cc201434428be7beee2190f95685",
  "database": "jb-sw-realm-6b47cc201434428be7beee2190f95685",
  "userRecordId": "e9ed6320d69644d8d2b49a4ea4012262165ce683000cd36abcac63aa21484178",
  "encrypted": true,
  "version": 12,
  "registrationState": "Registered",
  "registeredVersionHash": "6ea7457ea08fa6904b3ec5afbcd851b5b1d279c3bb097c919bf65df6334a4308",
  "guessCount": 0,
  "numGuesses": 10
}
```

* 这个工具只读，输出里不含密钥。
* 如果提示是明文旧记录（KMS 加密上线之前写入的），确认 env 文件就是这个 realm 的之后，加 `-allow-unencrypted` 再执行。

两次输出的 `registeredVersionHash` 不同，说明已经分叉。接着确定回滚方向：

* 常见情况是一个 realm 没收到 `Register2`，仍是旧版本。**收到了 `Register2` 的那个 realm 需要回滚**，回滚到另一个 realm 的当前版本。
* 把**另一个 realm** 输出的 `registeredVersionHash` 记为 `<hash>`。
* 把**要回滚的 realm** 输出的 `version` 记为 `<V>`，并记下它输出的 `database`，后面在 DBW 里要连这个库。

如果判断不出方向，或者两个 realm 都需要回滚，停在这一步找开发。

#### 第 4 步 找到要恢复的备份 `<historyId>`、`<edk>`（DBW，要回滚的 realm 的库）

```js
db.userRecordHistory.find({ userRecordId: "<recordId>", versionChanged: true, "before.registeredVersionHash": "<hash>" }).sort({ archivedAt: -1 })
```

取第一条（最新的一条），记下两个值：

* 它的 `_id`（24 位十六进制字符串），就是 `<historyId>`；
* 它的 `record.encryptedDataKey`，就是 `<edk>`。恢复命令会用它核对选中的确实是这条备份。

查不到，或者这条备份的 `record` 里没有 `encryptedDataKey`（KMS 加密上线之前的明文旧记录），停下来找开发。

#### 第 5 步 备份当前记录，得到 `<backupId>`、`<backupEdk>`（DBW，要回滚的 realm 的库）

这一步把当前记录完整存进 `userRecordHistory`，恢复错了可以用它撤销：

```js
db.userRecords.aggregate([
  { $match: { _id: "<recordId>" } },
  { $project: { _id: 0, userRecordId: "$_id", archivedAt: "$$NOW", manualBackup: { $literal: true }, record: "$$ROOT" } },
  { $merge: { into: "userRecordHistory", whenNotMatched: "insert" } }
])
```

这条命令执行成功时没有返回内容。再执行下面这条，确认备份已经存入，并把结果里的 `_id` 记为 `<backupId>`，`record.encryptedDataKey` 记为 `<backupEdk>`：

```js
db.userRecordHistory.find({ userRecordId: "<recordId>", manualBackup: true }).sort({ archivedAt: -1 }).limit(1)
```

#### 第 6 步 恢复（DBW，要回滚的 realm 的库）

这条命令把 `<historyId>` 那条备份里的文档整份写回 `userRecords`，并把 `version` 设为当前值加 1。命令里要替换 `<historyId>`、`<edk>`，以及 `<V>`（第 3 步记下的 version，填数字，不加引号）：

```js
db.userRecordHistory.aggregate([
  { $match: { userRecordId: "<recordId>", "record.encryptedDataKey": "<edk>", $expr: { $eq: [ { $toString: "$_id" }, "<historyId>" ] } } },
  { $replaceWith: "$record" },
  { $merge: {
      into: "userRecords",
      on: "_id",
      whenMatched: [ { $replaceWith: { $cond: [
          { $eq: [ "$version", <V> ] },
          { $mergeObjects: [ "$$new", { version: { $toLong: { $add: [ "$version", 1 ] } } } ] },
          "$$ROOT"
      ] } } ],
      whenNotMatched: "discard"
  } }
])
```

* 这条命令执行成功时没有返回内容，是否生效看第 7 步。
* 两重检查，任何一项不满足时它什么都不改，也不会报错：
  * 选中的备份必须同时符合 `<historyId>` 和 `<edk>`，所以 `<historyId>` 写错不会恢复成别的备份；
  * 当前 `version` 必须仍等于 `<V>`，所以第 3 步之后如果该用户又有新的写入，不会被覆盖。
* **一定要在要回滚的那个 realm 的库里执行。** 不能把一个 realm 的文档拷到另一个 realm，注册数据和 KMS 加密上下文都和 realm 绑定。
* 不要删掉 `$toLong`：它保证 `version` 仍是 64 位整数。如果变成小数或字符串，之后这个用户的所有写入请求（例如输入 PIN）都会失败。

#### 第 7 步 核对恢复结果

1. 在 **DBW（要回滚的 realm 的库）** 里执行：
   ```js
   db.userRecords.find({ _id: "<recordId>", version: { $type: "long" } })
   ```
   必须查到 1 条，`encryptedDataKey` 等于 `<edk>`，并且 `version` 等于 `<V>` 加 1。

   **只要有一项不满足，结果就是不确定的**：恢复可能没有执行（被检查拦下了），也可能执行后又发生了写入。这时不要撤销，也不要重试，保留 `<backupId>`、`<backupEdk>`，停下来找开发。
2. 在**要回滚的 realm 的服务器上**执行 `sudo ./record_state -env-file <env 文件> -record <recordId>`，输出的 `registeredVersionHash` 必须等于 `<hash>`。
3. 在**另一个 realm 的服务器上**也执行同样的命令，输出的 `registeredVersionHash` 必须仍等于 `<hash>`。如果变了，说明这期间另一个 realm 又被写入过（例如用户之前在途的请求），两边会再次不一致，停下来找开发。

**第 1 项满足、但第 2 项不满足时撤销**：在同一个库里再执行一次第 6 步的命令，把 `<historyId>` 换成 `<backupId>`，`<edk>` 换成 `<backupEdk>`，`<V>` 换成 `<V>` 加 1。记录会回到恢复前的内容，然后找开发。

#### 第 8 步 解除暂停并验证

1. 第 7 步三项都通过后，通知用户可以登录了，让用户用**旧 PIN** 恢复一次钱包。
2. 用户恢复成功后，在**两台 realm 服务器上**再各执行一次 `sudo ./record_state -env-file <env 文件> -record <recordId>`，两次输出的 `registeredVersionHash` 都应该等于 `<hash>`。

## Metrics and Tracing

If you want to gather metrics and tracing information from your realm, it is configured to report to a GRPC server at `OPENTELEMETRY_ENDPOINT`. The `Dockerfile` in this repo is configured to launch `jb-sw-realm` alongside an Open Telemetry Collector, which is one way you can gather this info. You can edit the `otel-collector-config.yml` to customize the export settings to your liking – by default, it expects a `DD_API_KEY` and `DD_SITE` environment variable to export to Datadog.

If you are using the Dockerized version in AWS ensure that the "Disable IMDSv1"
option is checked in the configuration. Otherwise the process inside the docker
container does not have access to IMDSv2 and renewing access tokens will be slow
while it times out IMDSv2 and falls back to IMDSv1. Checking the "Disable IMDSv1"
option will cause EC2 instance to have the metadata hop limit set to 2, allowing
access to the metadata service from within the docker container.

### GCP

To deploy to App Engine Flex with tracing enabled, you can deploy a docker image instead of allowing Google to build an image for you. You will need to set up a repository in Artifact Registry to push your image to.

This looks something like the following:
```sh
docker build --platform=linux/amd64 -t jb-sw-realm .
docker tag jb-sw-realm us-west1-docker.pkg.dev/${GCP_PROJECT_ID}/${ARTIFACT_REPOSITORY_NAME}/jb-sw-realm
docker push us-west1-docker.pkg.dev/${GCP_PROJECT_ID}/${ARTIFACT_REPOSITORY_NAME}/jb-sw-realm
gcloud app deploy --appyaml=app.yaml --image-url=us-west1-docker.pkg.dev/${GCP_PROJECT_ID}/${ARTIFACT_REPOSITORY_NAME}/jb-sw-realm
```

Note: Make sure to configure `OPENTELEMETRY_ENDPOINT` to `localhost:4317` in your `app.yaml` as well as adding any other environment parameters you need, such as `DD_API_KEY`.

### AWS

To deploy to Elastic Beanstalk with tracing enabled, you can update your environment to use the Docker solution stack instead of the Go solution stack. Additionally, you will need to define the appropriate additional environment properties on your configuration.
