# Jungle Gaming — execução e validação

Este README é o roteiro de avaliação do projeto: sobe o ambiente local, mostra
como autenticar e percorrer o fluxo financeiro sem seed, e separa os comandos
rápidos da bateria E2E isolada. O enunciado técnico original foi preservado em
[`docs/technical-challenge.md`](docs/technical-challenge.md).

## Subir o projeto localmente

Há dois jeitos intencionalmente separados de conhecer o sistema:

- o **ambiente manual** usa Docker Compose, mantém os dados locais entre reinícios e serve para percorrer a API pelo Swagger ou por chamadas HTTP;
- a **bateria E2E** usa Testcontainers e cria tudo do zero para cada execução. Ela não aproveita o banco, as filas, os volumes nem as APIs do Compose.

O `.env` é propositalmente público e versionado neste desafio. Ele contém apenas
credenciais locais de Keycloak e LocalStack, usadas para reduzir o tempo de setup
na avaliação; não há segredos de produção nele. `.env.example` é a referência dos
mesmos valores. Para usar outras portas ou valores locais, ajuste o `.env` na sua
máquina sem adicionar credenciais reais ao repositório. Em seguida:

```sh
docker compose up --build -d
docker compose ps
curl --fail-with-body http://localhost:8080/health/ready
```

Espere o `migrate` terminar com sucesso e as três APIs aparecerem como em execução. A resposta de readiness deve informar `database`, as duas filas SQS e `identityProvider` como `up`. A partir daí, abra `http://localhost:8080/swagger/index.html` ou siga o fluxo HTTP abaixo.

## O que o Compose inicia

O comando de subida executa o serviço `migrate` com `golang-migrate` antes de
iniciar as APIs. Em um banco criado pela versão anterior, ele apenas acrescenta
a coluna interna `dirty` na tabela de controle, preservando a versão já aplicada.

## Banco manual sem seed

As migrations versionadas criam somente schema: elas não inserem carteiras,
transações, ledger, inbox ou outbox. Portanto, a primeira execução de
`docker compose up --build -d` começa sem registros de domínio e o fluxo pode
ser executado integralmente pelo Swagger ou pela API.

Para descartar dados locais de uma execução manual anterior e voltar a esse
estado vazio, pare o ambiente e remova **somente** seus volumes locais:

```sh
docker compose down -v
docker compose up --build -d
```

Isso apaga os dados locais do Compose; não é necessário nem é usado pelos testes
E2E.

## Acesso local pronto para uso

Estes valores são intencionalmente públicos e existem somente para facilitar a
avaliação local. Não há IAM nem segredo de produção neste projeto.

| Serviço | Endereço | Credencial |
| --- | --- | --- |
| Swagger | http://localhost:8080/swagger/index.html | Use **Authorize** e escolha o fluxo desejado abaixo. |
| Keycloak Admin | http://localhost:8081 | `admin` / `admin-local` |
| Swagger interno | OAuth client `swagger-internal` | `wallet-tester` / `wallet-tester-local` |
| Swagger provider A | OAuth client `swagger-provider-a` | `provider-a-tester` / `provider-a-tester-local` |
| Swagger provider B | OAuth client `swagger-provider-b` | `provider-b-tester` / `provider-b-tester-local` |
| Swagger provider C | OAuth client `swagger-provider-c` | `provider-c-tester` / `provider-c-tester-local` |
| Provider A service-to-service | client `provider-a` | secret `provider-a-local-secret` |
| Provider B service-to-service | client `provider-b` | secret `provider-b-local-secret` |
| Provider C service-to-service | client `provider-c` | secret `provider-c-local-secret` |
| Carteiras service-to-service | client `wallet-service` | secret `wallet-service-local-secret` |
| LocalStack SQS | http://localhost:4566 | access key `test`; secret `test`; região `us-east-1` |

As filas são criadas automaticamente: `wager-transactions.fifo`, sua DLQ,
`wager-events.fifo` e sua DLQ. Para testar manualmente no Swagger, autentique
com `swagger-internal` para abrir/consultar carteiras e com
`swagger-provider-a`, `swagger-provider-b` ou `swagger-provider-c` para enviar
ou consultar apostas.

## Fluxo manual completo, sem dados pré-criados

O banco da aplicação começa vazio. O roteiro abaixo cria o jogador, a carteira e
uma aposta; não depende de IDs ou transações deixadas por uma execução anterior.
Ele usa `client_credentials`, como uma integração de serviço faria. Os clients e
segredos são deliberadamente locais e públicos para a avaliação. O exemplo usa
`curl`, `jq` e `uuidgen` somente no computador que está chamando a API; a API,
o banco, Keycloak e SQS seguem todos nos containers do Compose.

```sh
token() {
  curl --silent --show-error --fail \
    --user "$1:$2" \
    --data "grant_type=client_credentials" \
    http://localhost:8081/realms/gaming/protocol/openid-connect/token
}

export INTERNAL_TOKEN="$(token wallet-service wallet-service-local-secret | jq -r '.access_token')"
export PROVIDER_A_TOKEN="$(token provider-a provider-a-local-secret | jq -r '.access_token')"
export PLAYER_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"

WALLET_JSON="$(curl --silent --show-error --fail-with-body \
  -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  --data @- <<JSON
{"playerId":"$PLAYER_ID","initialBalance":{"amount":"100.00","currency":"BRL"}}
JSON
)"
export WALLET_ID="$(printf '%s' "$WALLET_JSON" | jq -r '.id')"
export EXTERNAL_TRANSACTION_ID="manual-bet-$(uuidgen)"
export ROUND_ID="manual-round-$(uuidgen)"

curl --silent --show-error --fail-with-body \
  -X POST http://localhost:8082/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Idempotency-Key: provider-a:$EXTERNAL_TRANSACTION_ID" \
  -H 'Content-Type: application/json' \
  --data @- <<JSON
{
  "providerId":"provider-a",
  "externalTransactionId":"$EXTERNAL_TRANSACTION_ID",
  "playerId":"$PLAYER_ID",
  "walletId":"$WALLET_ID",
  "roundId":"$ROUND_ID",
  "gameId":"fortune-chimp",
  "kind":"BET",
  "money":{"amount":"25.00","currency":"BRL"}
}
JSON

curl --silent --show-error --fail-with-body \
  "http://localhost:8083/providers/provider-a/wagering/transactions/$EXTERNAL_TRANSACTION_ID" \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN"
curl --silent --show-error --fail-with-body \
  "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=100" \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
curl --silent --show-error --fail-with-body \
  -X POST "http://localhost:8082/wallets/$WALLET_ID/reconciliation" \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
```

O resultado esperado é a aposta em `PROCESSED`, saldo `75.00 BRL`, uma abertura
e um débito no ledger, e reconciliação consistente. Note que cada leitura vai
para uma instância diferente da API: `8080`, `8082` e `8083`. Reenvie exatamente
o mesmo `POST` com a mesma `Idempotency-Key` para observar
`idempotentReplay: true` sem um segundo débito. Trocar somente o token para o
client `provider-b` e tentar consultar a transação de `provider-a` deve devolver
`403 PROVIDER_FORBIDDEN`.

Se `jq` ou `uuidgen` não estiverem instalados, gere um UUID e copie o campo
`access_token` do JSON retornado pelo endpoint do Keycloak. Eles aparecem no
exemplo apenas para deixar o roteiro copiável; não são dependências da aplicação.

Para acompanhar as mensagens de saída, a fila é `wager-events.fifo`; os formatos
e as regras de deduplicação do consumidor estão em `docs/outbound-event-contract.md`.
O Swagger é preferível para uma exploração manual dos demais tipos (`WIN`, `LOSS`,
`REFUND` e `ROLLBACK`), pois permite alterar apenas o corpo e manter o mesmo
fluxo autenticado.

## Health checks

`GET /health/live` confirma somente que o processo está em execução. Use
`GET /health/ready` para diagnosticar dependências: ele informa o estado e a
latência de `database`, `sqsInputQueue`, `sqsEventQueue` e
`identityProvider`. O resultado é `200` quando todos estão disponíveis e
`503` com os checks parciais quando qualquer dependência estiver indisponível.

```json
{
  "status": "ready",
  "checkedAt": "2026-09-09T07:00:00Z",
  "checks": {
    "database": { "status": "up", "latencyMs": 2 },
    "sqsInputQueue": { "status": "up", "latencyMs": 5 },
    "sqsEventQueue": { "status": "up", "latencyMs": 5 },
    "identityProvider": { "status": "up", "latencyMs": 3 }
  }
}
```

## Migrations

Aplicar migrations manualmente (o `up` é também o padrão do Compose):

```sh
docker compose run --rm migrate
```

Reverter uma única versão — pare as APIs antes para não executar schema e
aplicação em versões diferentes:

```sh
docker compose stop api-1 api-2 api-3
docker compose run --rm migrate down 1
```

Para retornar ao estado mais recente, execute novamente `docker compose up -d`.

A migração `000004_wallet_ledger_consistency` adiciona validações deferidas no
commit: o saldo persistido deve corresponder à soma do ledger e toda operação
financeira `PROCESSED` precisa ter lançamento. Assim, uma escrita SQL direta de
saldo sem o ledger é rejeitada pelo próprio PostgreSQL.

## E2E isolado com Testcontainers

Os testes com a tag `integration` não usam os serviços do Compose. O `TestMain`
cria uma rede privada e efêmera com Testcontainers, PostgreSQL novo, LocalStack,
Keycloak e três processos da API em containers independentes. Aplica as migrations
em um banco de teste vazio e confere que não há dados de domínio antes do primeiro
cenário. O único fixture é o realm do Keycloak, limitado aos clients OAuth usados
nos testes. Ao fim da suíte, todos os containers, rede e imagem de teste são
removidos automaticamente.

```sh
go test -count=1 -tags=integration ./integration -v
```

No Windows, execute o detector de corrida em Linux. O container abaixo é apenas
o executor de testes: as dependências e as APIs continuam sendo criadas e
encerradas pelo Testcontainers, sem iniciar o Compose.

```sh
docker build --no-cache -f Dockerfile.test -t jungle-gaming-e2e-test .
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
  jungle-gaming-e2e-test test -count=1 -race -tags="integration hostrecovery" ./integration -v
```

Além de concorrência e recuperação, a suíte cria uma transação real com o
client OAuth `provider-b` e confirma que o token de `provider-a` não consegue
consultá-la nem reexecutá-la. A rejeição não cria nova transação nem altera
carteira ou ledger; o replay pelo provider B continua idempotente. O lifecycle
Fx é iniciado com os workers reais antes de realizar o shutdown. Também confirma
que o client OAuth `provider-c` é aceito pela API com seu claim de provider.
Ela também simula um publisher interrompido depois de `SendMessage` e antes de
confirmar a outbox: após a expiração do lease, outro publisher recupera o mesmo
registro, preserva o `eventId` e confirma a publicação.

Os endereços usados pelo teste são resolvidos dinamicamente; nenhum endpoint,
volume ou fila do ambiente manual é reutilizado.

## Cenários de recuperação que controlam containers

Os dois cenários abaixo interrompem e recriam containers de API do próprio
Testcontainers. Eles não tocam no Compose:

```sh
go test -tags="integration hostrecovery" ./integration -run "TestConsumerRedeliversAfterCommitBeforeDelete|TestRestartPreservesIdempotencyAndPendingReference" -v -count=1
```

Eles cobrem, respectivamente, a queda abrupta entre o commit e a remoção da
mensagem SQS e a preservação de idempotência/referências pendentes após reiniciar
as APIs.

## Outros comandos

```sh
go test -count=1 ./...
go vet ./...
docker compose down
```

`go test -count=1 ./...` é a verificação rápida do código sem dependências
externas. A execução com Testcontainers é a validação de sistema: ela chama as
APIs reais, obtém tokens reais do Keycloak, usa SQS real do LocalStack e aplica
migrations em um PostgreSQL vazio. Rode a versão Linux com `-race` antes de uma
entrega; ela também inclui os cenários que reiniciam containers durante o fluxo.

Para parar o ambiente manual sem perder a sessão, use `docker compose down`.
Para voltar deliberadamente ao banco vazio, use `docker compose down -v` e suba
novamente. Esse segundo comando remove os volumes locais do Compose, mas nunca é
necessário para os testes E2E.
