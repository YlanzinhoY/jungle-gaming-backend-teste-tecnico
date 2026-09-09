# Testes

## Pré-requisito

Inicie a infraestrutura e as três instâncias da API:

```sh
docker compose up --build -d
```

O Compose executa o serviço `migrate` com `golang-migrate` antes de iniciar as
APIs. Em um banco criado pela versão anterior, ele apenas acrescenta a coluna
interna `dirty` na tabela de controle, preservando a versão já aplicada.

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

## Testes com detector de corrida

No Windows, o `-race` precisa de CGO e GCC. O serviço `test-race` fornece os dois
em Linux e executa testes unitários e de integração contra PostgreSQL, Keycloak,
LocalStack/SQS e as três APIs reais:

```sh
docker compose --profile test run --rm test-race
```

Além de concorrência e recuperação, a suíte cria uma transação real com o
client OAuth `provider-b` e confirma que o token de `provider-a` não consegue
consultá-la nem reexecutá-la. A rejeição não cria nova transação nem altera
carteira ou ledger; o replay pelo provider B continua idempotente. O lifecycle
Fx é iniciado com os workers reais antes de realizar o shutdown. Também confirma
que o client OAuth `provider-c` é aceito pela API com seu claim de provider.

O container usa a rede interna do Compose. Os endereços públicos continuam em
`localhost`; por isso o teste usa os nomes internos dos serviços apenas durante
sua execução.

## Cenários de recuperação que controlam containers

Os dois cenários abaixo interrompem e recriam instâncias do próprio Compose. Eles
devem ser executados pelo host, que tem acesso ao Docker Desktop:

```powershell
go test -tags="integration hostrecovery" ./integration -run "TestConsumerRedeliversAfterCommitBeforeDelete|TestRestartPreservesIdempotencyAndPendingReference" -v -count=1
```

Eles cobrem, respectivamente, a queda entre o commit e a remoção da mensagem SQS
e a preservação de idempotência/referências pendentes após reiniciar as APIs.

## Outros comandos

```sh
go test ./...
go vet ./...
docker compose down
```
