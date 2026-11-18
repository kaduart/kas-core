// Stand-in for infrastructure/queue/queueConfig.js. getQueue(name).add keeps
// BullMQ's observable rule for a custom jobId: adding a job whose id already
// exists does not create a second job (it returns the existing one). The
// failure plan comes from the fictitious payload: payload.fail is a list of
// booleans consumed one per add() call for that event; true throws.
export const jobs = new Map(); // jobId -> { queue, eventId }
export const stats = { adds: 0, duplicatesSuppressed: 0, thrown: 0 };
const consumed = new Map(); // eventId -> index into payload.fail

export function getQueue(queueName) {
  return {
    name: queueName,
    async add(_name, data, opts) {
      stats.adds++;
      const plan = (data.payload && data.payload.fail) || [];
      const i = consumed.get(data.eventId) || 0;
      consumed.set(data.eventId, i + 1);
      if (plan[i] === true) {
        stats.thrown++;
        throw new Error('Connection is closed.');
      }
      if (jobs.has(opts.jobId)) {
        stats.duplicatesSuppressed++;
        return { id: opts.jobId };
      }
      jobs.set(opts.jobId, { queue: queueName, eventId: data.eventId });
      return { id: opts.jobId };
    }
  };
}
