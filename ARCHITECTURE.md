# Arquitetura

## Visão geral

O serviço usa DDD com Clean Architecture. O domínio não importa Fx, HTTP, PostgreSQL, OIDC nem AWS. A direção das dependências é:

```text
cmd/api -> transport + infrastructure -> application -> domain
                                      \-> application ports
```

- `internal/domain`: `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry`, invariantes e transições.
- `internal/application`: casos de uso, contratos de entrada/saída e portas de persistência.
- `internal/infrastructure`: adaptadores PostgreSQL, OIDC e SQS.
- `internal/transport/httpapi`: contrato HTTP, autorização na borda e anotações Swaggo.
- `docs/swagger`: Swagger 2.0 gerado pela CLI `swag` e servido por `http-swagger`.
- `docs/http-error-contract.md`: envelope, códigos e semântica de falhas HTTP.
- `docs/outbound-event-contract.md`: contrato versionado de publicação e consumo da outbox.
- `internal/composition`: módulos Fx de foundation, persistência, aplicação, identidade, mensageria e HTTP.
- `cmd/api`: composition root com Uber Fx.

Fx aparece apenas no composition root e nos adaptadores que registram lifecycle hooks. O domínio permanece Go puro.

Validações estruturais dos comandos (campos obrigatórios, UUIDs, limites, tipos aceitos e metadados da inbox) usam `go-playground/validator` por tags declarativas. As invariantes financeiras e transições continuam no domínio para que nenhuma outra entrada consiga contorná-las.

## Dinheiro

`Money` guarda o valor em `int64` de unidades mínimas e uma moeda ISO 4217. A escala externa é obrigatoriamente duas casas. O parser não aceita sinal, expoente, `NaN`, infinito, espaços, escala adicional nem arredondamento. Soma, subtração e negação verificam overflow e exigem moedas iguais. O PostgreSQL persiste os centavos em `BIGINT` e a moeda separadamente.

O hash idempotente recebe a forma normalizada `Money.Amount()` (`25.00`) e nunca usa ponto flutuante.

## Limite transacional e concorrência

Uma chamada financeira executa em uma única transação PostgreSQL. Conforme o caso, ela contém:

1. claim da inbox;
2. lock consultivo transacional da chave `(providerId, idempotencyKey)`;
3. validação dos registros idempotentes;
4. `SELECT ... FOR UPDATE` da carteira;
5. operação, saldo, ledger e estado terminal;
6. eventos da outbox;
7. conclusão da inbox.

O lock consultivo serializa somente a mesma chave idempotente. Sua chave inteira vem dos primeiros 64 bits de SHA-256 sobre `providerId`, um byte separador e `idempotencyKey`; nenhum separador NUL é enviado como texto ao PostgreSQL. O row lock serializa somente escritores da mesma carteira; carteiras diferentes progridem em paralelo. O `UPDATE` da carteira também exige a versão anterior (`WHERE version = novaVersao - 1`), impedindo lost updates mesmo diante de erro de programação. Não há mutex ou lock global em memória.

O schema reforça saldo não negativo, aritmética do ledger, unicidade de carteira por jogador/moeda, transação externa e idempotência. O trigger `wallet_ledger_no_update` recusa `UPDATE` e `DELETE` no ledger. Outro guard permite inserir ledger apenas para transação `PROCESSED` com movimento, confere carteira, moeda, valor, saldo final e direção esperada; assim `LOSS`, `REJECTED` e `FAILED` não podem receber lançamento nem por escrita SQL direta. Triggers de restrição deferidos validam no commit que o saldo final da carteira é exatamente a soma de créditos menos débitos do ledger e que toda transação financeira `PROCESSED` possui seu lançamento; uma alteração direta de saldo ou uma transação processada sem ledger é rejeitada pelo PostgreSQL.

## Idempotência

O hash SHA-256 usa um JSON canônico montado como mapa de strings; `encoding/json` ordena suas chaves lexicograficamente. Os campos são provedor, ID externo, jogador, carteira, rodada, jogo, tipo, valor normalizado, moeda e referência. O header de idempotência e metadados HTTP/SQS não entram no hash. HTTP e SQS constroem exatamente o mesmo comando.

- Mesma chave e hash: retorna o estado persistido com `idempotentReplay=true`.
- Mesma chave e hash diferente: `409 IDEMPOTENCY_PAYLOAD_CONFLICT`.
- Mesmo `(providerId, externalTransactionId)` com outra chave: `409 EXTERNAL_TRANSACTION_CONFLICT`.
- Resultado terminal: usa `result_balance_minor`, preservando o saldo observado no processamento original.

No SQS, `(consumer_name, message_id)` identifica a inbox. Um SHA-256 do envelope recebido detecta reutilização do `messageId` com outro corpo. A conclusão da inbox ocorre no mesmo commit financeiro.

## Operações, referências e reversões

`BET` debita, `WIN` credita, `LOSS` não movimenta, `REFUND` credita o valor integral da aposta e `ROLLBACK` aplica a direção contrária da operação referenciada. `ROLLBACK(BET)` credita; `ROLLBACK(WIN|REFUND)` debita. Débito insuficiente de uma aposta usa `INSUFFICIENT_FUNDS`; débito insuficiente de reversão usa `INSUFFICIENT_FUNDS_REVERSAL`.

Uma referência precisa concordar em provedor, jogador, carteira, moeda e rodada. `WIN` aceita somente referência a `BET`, mas o prêmio pode ter valor diferente; igualdade integral é exigida para `REFUND` e `ROLLBACK`. `REFUND` aceita somente `BET`; `ROLLBACK`, `BET`, `WIN` ou `REFUND`.

Uma transação `REJECTED` por jogador ou moeda incompatível preserva no registro os valores recebidos para auditoria. Por isso, o banco mantém uma FK simples para a carteira e um trigger exige a identidade composta `(wallet, player, currency)` em todos os estados exceto `REJECTED`; a exceção não permite qualquer movimento financeiro.

A política para combinações é uma reversão financeira bem-sucedida por transação referenciada, independentemente de ser `REFUND` ou `ROLLBACK`. O índice parcial `wager_successful_reversal_uq` impõe isso no banco. É permitido fazer rollback de um refund processado, pois nesse caso a referência é a transação de refund, não a aposta original. Isso reverte o crédito sem permitir uma segunda devolução direta da aposta.

Referência ausente ou ainda pendente produz `PENDING_REFERENCE`, evento próprio e `next_attempt_at`. O worker usa backoff exponencial de 2 segundos até 5 minutos. Após 10 tentativas, rejeita com `REFERENCE_NOT_FOUND`. Referência terminal sem sucesso usa `REFERENCE_NOT_PROCESSED`. Os limites são configuráveis.

## Inbox, SQS e DLQ

O AWS SDK usa a API real do SQS. `AWS_ENDPOINT_URL` permite apontar para LocalStack ou MiniStack local; nenhuma simulação em memória substitui o broker. Na inicialização, o serviço cria ou resolve:

- `wager-transactions.fifo` e `wager-transactions-dlq.fifo`;
- `wager-events.fifo` e `wager-events-dlq.fifo`.

As filas principais recebem redrive após cinco recebimentos e visibility timeout de 30 segundos. Entradas bem-sucedidas e rejeições de negócio duráveis são removidas somente depois do commit. Entradas inválidas e falhas permanentes não são removidas, chegando à DLQ pelo redrive. Falhas transitórias também permanecem para retry.

Para entradas, o produtor deve usar `MessageGroupId=walletId` e `MessageDeduplicationId=messageId`. Isso preserva ordem por carteira sem impedir paralelismo entre carteiras. A aplicação não depende da deduplicação FIFO para integridade.

No encerramento, o cancelamento interrompe novas long polls. A chamada em processamento recebe o mesmo contexto; se o commit não ocorrer, a mensagem não é removida e retorna após o visibility timeout.

## Transactional outbox

Todo evento externo nasce na mesma transação de banco que seu fato. Publishers concorrentes reivindicam lotes via `FOR UPDATE SKIP LOCKED`, gravam `locked_by/locked_until` e publicam somente depois do commit original. Falhas liberam o registro com backoff. Uma lease expirada permite que outra instância recupere trabalho abandonado.

Eventos de saída usam `MessageGroupId=aggregateId` e `MessageDeduplicationId=eventId`. Se o processo cair depois do publish e antes de marcar a outbox, o mesmo `eventId` será republicado; consumidores devem ser idempotentes. Essa é entrega at-least-once, sem falsa promessa de exactly-once distribuído.

Na entrada, a aplicação lê `ApproximateReceiveCount` e calcula backoff exponencial limitado para erros transitórios. Erros permanentes recebem visibilidade zero para alcançarem rapidamente o redrive configurado. Um heartbeat estende a visibilidade durante o caso de uso; cancelamento por shutdown libera a mensagem. A criação das filas é idempotente e os atributos mutáveis de visibilidade e redrive são reconciliados em cada inicialização quando `SQS_CREATE_QUEUES=true`.

## Eventos

O envelope contém `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId`, `occurredAt`, `version` e `data`. Construtores internos fixam tipo e versão. Os eventos são:

- `WagerTransactionProcessed` (inclui `LOSS` e `OPENING`);
- `WagerTransactionRejected`;
- `WagerTransactionFailed`;
- `WalletBalanceChanged`;
- `WagerTransactionPendingReference`.

O payload serializado é persistido como snapshot JSONB imutável depois de publicado.

## Autenticação e autorização

Issuer e discovery podem ter endereços distintos: OIDC_ISSUER_URL é a identidade canônica exigida no token e OIDC_DISCOVERY_URL é um backchannel opcional. No Compose, isso permite validar o issuer público localhost:8081 enquanto discovery e JWKS trafegam na rede interna pelo serviço keycloak; a substituição não desativa a checagem do claim iss.

O serviço faz discovery OIDC e valida assinatura, emissor, expiração e audiência. Não emite tokens e não armazena senhas. O client credentials do provedor recebe o claim configurável `provider_id`; o corpo e a rota devem corresponder a esse claim. Replays passam pela mesma verificação.

Endpoints de carteira, ledger, reconciliação e consulta por ID interno exigem a role de realm `wallet-service`. Health checks e Swagger são públicos. A configuração local de exemplo fica em `docs/keycloak-realm.json`.

## Estado e falhas

Operações externas nascem em `PENDING`, mas as que não dependem de referência são finalizadas no mesmo commit; por isso não existe `PENDING` confirmado sem retomada. `PENDING_REFERENCE` é o único estado intermediário confirmado e possui worker durável. `PROCESSED`, `REJECTED` e `FAILED` são terminais no domínio. Rejeições de negócio conhecidas são persistidas. Falhas técnicas do worker são adiadas de forma durável e, ao esgotarem o limite configurado, produzem `FAILED/PROCESSING_ATTEMPTS_EXHAUSTED` e evento próprio sem lançamento no ledger. Indisponibilidade de PostgreSQL/SQS continua causando retry sem fabricar um resultado de negócio.

## Lifecycle e shutdown

Fx constrói configuração, pool, repositórios, casos de uso, OIDC, SQS, HTTP e workers. Hooks inicializam discovery e filas antes de servir tráfego. No shutdown, HTTP deixa de aceitar entradas, workers recebem cancelamento e o pool fecha por último conforme a ordem reversa do lifecycle.

O serviço `migrate` do Docker Compose executa `golang-migrate` antes das APIs. As migrations SQL versionadas continuam em `internal/infrastructure/postgres/migrations`; a aplicação não altera schema durante seu startup. Os comandos de aplicação e reversão estão em `TESTING.md`.

## Ambiente local

O `compose.yaml` inicia PostgreSQL da aplicação, PostgreSQL isolado do IdP, Keycloak com import automático do realm, LocalStack limitado a SQS e três instâncias da API. A imagem da aplicação é multi-stage, contém somente os binários e certificados necessários e executa com usuário sem privilégios. Volumes nomeados preservam banco, identidade e filas entre reinícios.

## Estratégia de testes

Os testes unitários exercitam o domínio puro, hash/cursor da aplicação, retry da mensageria, métricas e o grafo Fx. A build tag `integration` sobe uma rede isolada por Testcontainers com PostgreSQL, LocalStack, Keycloak e três containers independentes da API; não reutiliza os containers, filas, banco ou volumes do Compose manual. Antes dos cenários, as migrations do `golang-migrate` são aplicadas em um PostgreSQL novo e o teste confirma que wallets, transações, ledger, inbox e outbox começam vazios. O realm importado no Keycloak é o único fixture e se limita aos clients OAuth de teste. Concorrência financeira é validada pelo estado final e pelo ledger, não por detalhes de implementação em memória. A recuperação da outbox também é exercitada no intervalo entre o `SendMessage` bem-sucedido e sua confirmação no banco: um lease abandonado é retomado por outro publisher com o mesmo `eventId` e payload imutável.

O detector de corrida é executado em Linux porque o Go para Windows exige CGO e um compilador C. O `Dockerfile.test` instala GCC e pode executar a suíte contra o Docker socket; as dependências continuam sendo criadas e removidas pelo Testcontainers, sem perfil de testes do Compose. Os dois testes de recuperação que controlam o ciclo de vida dos containers usam a tag adicional `hostrecovery`, agora interrompendo e recriando exclusivamente as APIs efêmeras do Testcontainers; os comandos e a justificativa estão em `TESTING.md`. Tracing e testes de carga continuam como diferenciais opcionais do enunciado e não foram implementados.

## Observabilidade

Há logs JSON estruturados com IDs disponíveis e health checks de PostgreSQL/SQS. Cada instância expõe `/metrics` no formato Prometheus, com resultados por status, replays idempotentes, conflitos de concorrência, retries e encaminhamentos à DLQ, atraso da outbox, latência de processamento e divergências de reconciliação. O tracing distribuído usa OpenTelemetry com exportação OTLP/gRPC, spans de HTTP, PostgreSQL e SQS, propagação W3C Trace Context nas mensagens e correlação dos logs por `traceId`/`spanId`. Payloads financeiros e credenciais não são logados.
