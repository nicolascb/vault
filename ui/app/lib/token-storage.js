import localStorageWrapper from './local-storage';
import memoryStorage from './memory-storage';

export default function (type) {
  if (type === 'memory') {
    return memoryStorage;
  }
  let storage;
  try {
    localStorageWrapper.getItem('test');
    storage = localStorageWrapper;
  } catch (e) {
    storage = memoryStorage;
  }
  return storage;
}
