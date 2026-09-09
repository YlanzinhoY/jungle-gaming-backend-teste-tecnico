# Contrato de consumo dos eventos de saída

Os eventos de domínio confirmados são persistidos na outbox dentro da mesma
transação que a carteira, a transação e o ledger. Um publisher os entrega na
fila FIFO `wager-events.fifo` (configurável por `SQS_EVENT_QUEUE`); a DLQ padrão
é `wager-events-dlq.fifo`.

Este é um contrato para consumidores externos. O corpo da mensagem é o snapshot
JSON imutável salvo na outbox, e não há um segundo envelope SQS da aplicação.

## Transporte e entrega

| Item | Regra |
| --- | --- |
| Fila | FIFO, `wager-events.fifo` por padrão. |
| Corpo SQS | Um envelope de evento JSON UTF-8, descrito abaixo. |
| `MessageGroupId` | `aggregateId`: preserva a ordem de um agregado, não a ordem global. |
| `MessageDeduplicationId` | `eventId`. É uma otimização FIFO, não substitui idempotência do consumidor. |
| Atributos SQS | Podem conter `traceparent` e `tracestate` W3C para tracing. São opcionais para regra de negócio. |
| Garantia | At-least-once. O mesmo `eventId` pode ser republicado após queda entre publish e confirmação da outbox. |
| Ordem | Eventos da mesma carteira têm ordem no `WalletBalanceChanged`; eventos da mesma transação têm ordem no `WagerTransaction*`. Não compare ordem entre agregados distintos. |

O consumidor deve gravar `eventId` de forma durável junto de seu efeito antes de
remover a mensagem. Recebimentos repetidos do mesmo ID são sucesso sem repetir
o efeito. Mensagem malformada ou versão não suportada deve ir para a DLQ do
consumidor; indisponibilidade transitória deve deixar a mensagem disponível para
retry.

## Envelope versão 1

```json
{
  "eventId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "eventType": "WagerTransactionProcessed",
  "aggregateId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "correlationId": "request-7ca7d4cc",
  "causationId": "msg-123",
  "occurredAt": "2026-09-09T08:30:00.123456789Z",
  "version": 1,
  "data": {}
}
```

| Campo | Tipo e regra |
| --- | --- |
| `eventId` | UUID estável e único; chave de deduplicação do consumidor. |
| `eventType` | Discriminador do tipo de `data`. |
| `aggregateId` | ID do agregado usado para ordenação FIFO. É a transação nos eventos `WagerTransaction*` e a carteira em `WalletBalanceChanged`. |
| `correlationId` | Identificador de rastreio da operação. Em entrada HTTP vem de `X-Correlation-ID` ou `X-Request-ID`; em entrada SQS, do `messageId` recebido. |
| `causationId` | Opcional. É o `messageId` da entrada SQS ou o ID da transação que causou alteração de saldo. Ausente quando não aplicável. |
| `occurredAt` | UTC em RFC 3339 com precisão de nanossegundos. |
| `version` | Inteiro. O produtor atual emite somente `1`. |
| `data` | Objeto tipado de acordo com `eventType`. |

Todos os valores monetários usam o mesmo objeto, sem ponto flutuante:

```json
{ "amount": "25.00", "currency": "BRL" }
```

`amount` é string decimal de duas casas, e `currency` é ISO 4217 maiúscula.

## Tipos de evento e dados

| `eventType` | Gatilho | `aggregateId` | Campos de `data` |
| --- | --- | --- | --- |
| `WagerTransactionProcessed` | Operação concluída, incluindo `LOSS` e abertura interna com saldo inicial positivo. | `transactionId` | `transactionId`, `walletId`, `playerId`, `kind`, `money`, `status` (`PROCESSED`), `balance`; e, para origem externa, `externalTransactionId`, `providerId`, `roundId`, `gameId`. Pode conter `referenceTransactionId` e `referenceExternalTransactionId`. |
| `WagerTransactionRejected` | Rejeição terminal de negócio. | `transactionId` | `transactionId`, `externalTransactionId`, `providerId`, `walletId`, `kind`, `status` (`REJECTED`), `failureCode`, `balance`. |
| `WagerTransactionFailed` | Falha terminal de retomada, após `PROCESSING_ATTEMPTS_EXHAUSTED`. | `transactionId` | `transactionId`, `externalTransactionId`, `providerId`, `walletId`, `kind`, `status` (`FAILED`), `failureCode`, `balance`. |
| `WagerTransactionPendingReference` | Referência ainda não existe ou ainda é pendente. | `transactionId` | `transactionId`, `externalTransactionId`, `providerId`, `walletId`, `referenceExternalTransactionId`, `status` (`PENDING_REFERENCE`), `nextAttemptAt`. |
| `WalletBalanceChanged` | Crédito ou débito efetivamente persistido. Não é emitido para `LOSS`, `REJECTED`, `FAILED` nem saldo inicial zero. | `walletId` | `walletId`, `transactionId`, `direction` (`CREDIT` ou `DEBIT`), `money`, `balanceBefore`, `balanceAfter`, `walletVersion`. |

Campos opcionais do primeiro tipo são omitidos — não enviados como `null` — em
uma abertura interna (`kind: OPENING`), que não tem provider, ID externo, rodada,
jogo ou referência.

### Exemplo: transação processada

```json
{
  "eventId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "eventType": "WagerTransactionProcessed",
  "aggregateId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "correlationId": "request-7ca7d4cc",
  "occurredAt": "2026-09-09T08:30:00.123456789Z",
  "version": 1,
  "data": {
    "transactionId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
    "externalTransactionId": "bet-123",
    "providerId": "provider-a",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" },
    "status": "PROCESSED",
    "balance": { "amount": "75.00", "currency": "BRL" }
  }
}
```

### Exemplo: alteração de saldo correspondente

```json
{
  "eventId": "0192f320-15d8-742b-84fb-3f9c48e2d81d",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "correlationId": "request-7ca7d4cc",
  "causationId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "occurredAt": "2026-09-09T08:30:00.123456789Z",
  "version": 1,
  "data": {
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "transactionId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
    "direction": "DEBIT",
    "money": { "amount": "25.00", "currency": "BRL" },
    "balanceBefore": { "amount": "100.00", "currency": "BRL" },
    "balanceAfter": { "amount": "75.00", "currency": "BRL" },
    "walletVersion": 2
  }
}
```

## Relação entre eventos

Uma conclusão com movimento gera `WagerTransactionProcessed` e
`WalletBalanceChanged`. Os dois carregam o mesmo `transactionId`; o segundo
também informa esse ID em `causationId`. `LOSS` gera apenas o evento processado.
Uma rejeição gera somente `WagerTransactionRejected`. Uma referência pendente
gera `WagerTransactionPendingReference` e, mais tarde, pode gerar um evento
processado, rejeitado ou falho para a mesma transação.

Não é correto derivar saldo apenas de `WagerTransactionProcessed`: para isso o
consumidor deve usar `WalletBalanceChanged`, que declara a direção, os saldos
antes/depois e a versão da carteira.

## Evolução compatível

Consumidores devem aceitar campos adicionais desconhecidos na versão 1 e exigir
os campos listados para o respectivo `eventType`. Uma alteração incompatível
(remoção, troca de semântica/tipo ou novo conjunto obrigatório) requer nova
versão de envelope; consumidores que não a suportam devem falhar de maneira
permanente e encaminhar a mensagem à DLQ em vez de interpretá-la parcialmente.

