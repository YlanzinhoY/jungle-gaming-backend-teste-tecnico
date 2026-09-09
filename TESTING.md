# Testes

## Pré-requisito

Inicie a infraestrutura e as três instâncias da API:

```sh
docker compose up --build -d
```

O Compose executa o serviço `migrate` com `golang-migrate` antes de iniciar as
APIs. Em um banco criado pela versão anterior, ele apenas acrescenta a coluna
interna `dirty` na tabela de controle, preservando a versão já aplicada.

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
  jungle-gaming-e2e-test test -count=1 -race -tags=integration ./...
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
