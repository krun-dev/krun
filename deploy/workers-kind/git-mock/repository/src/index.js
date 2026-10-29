export default {
  fetch(request, env) {
    return Response.json({
      source: "repository-wrangler",
      environment: env.KRUN_ENVIRONMENT,
      url: request.url,
    });
  },
};
