export default function (env) {
  installEgressFetch(() => env.OUTBOUND);
  return env.OUTBOUND;
}
