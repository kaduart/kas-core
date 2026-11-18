// Stand-in for infrastructure/events/eventPublisher.js: only the map the
// dispatcher imports, with fictitious event types covering the three shapes
// (one queue, several queues, no queue). Anything else is UNKNOWN_EVENT_TYPE.
export const eventToQueueMap = {
  FICTICIO_UMA_FILA: 'fila-a',
  FICTICIO_DUAS_FILAS: ['fila-a', 'fila-b'],
  FICTICIO_SEM_FILA: []
};
